package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	// mediaFetchTimeout bounds the whole fetch of one external image, redirects included.
	mediaFetchTimeout = 10 * time.Second
	// mediaMaxBytes caps the size of an external image read to hash it.
	mediaMaxBytes = 10 << 20
	// mediaMaxRedirects caps how many redirects an external image fetch follows.
	mediaMaxRedirects = 3
)

// mediaIPAllowed decides which addresses an external image may be fetched from. It is checked
// when dialing, so neither a redirect nor a DNS answer that changes between lookups can reach an
// address it refuses. Tests swap it to also reach their loopback image server.
var mediaIPAllowed = isPublicIP

// cgnatRange is the shared address space (RFC 6598), not routable on the public internet.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isPublicIP reports whether ip is a public unicast address: not private, loopback, link-local,
// multicast, unspecified or shared (CGNAT) address space.
func isPublicIP(ip net.IP) bool {
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified() && !cgnatRange.Contains(ip)
}

// newMediaHTTPClient returns the HTTP client external images are fetched with: no proxy, every
// dialed address checked against allowed, at most mediaMaxRedirects redirects and only to http(s).
func newMediaHTTPClient(allowed func(net.IP) bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: mediaFetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil || !allowed(ip) {
				return fmt.Errorf("address %s is not allowed", host)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: mediaFetchTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   mediaFetchTimeout,
			ResponseHeaderTimeout: mediaFetchTimeout,
			MaxIdleConns:          1,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > mediaMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}

// fetchImage downloads an external image: an http(s) URL answering 200 with an image/* content
// type and at most mediaMaxBytes of body.
func fetchImage(client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return nil, fmt.Errorf("content type %q is not an image", resp.Header.Get("Content-Type"))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if len(data) > mediaMaxBytes {
		return nil, fmt.Errorf("image larger than %d bytes", mediaMaxBytes)
	}
	return data, nil
}

// imageHasher hashes images by content for election metadata documents, each url once however
// many documents or choices name it.
type imageHasher struct {
	a      *API
	client *http.Client
	cache  map[string]string
}

// newImageHasher returns an imageHasher with an empty cache.
func (a *API) newImageHasher() *imageHasher {
	return &imageHasher{a: a, cache: make(map[string]string)}
}

// hashes returns the lowercase hex SHA-256 of the content of each image url, keyed by url
// exactly as given; empty urls are skipped and nil is returned when there is none. An image this
// backend's object storage serves is read from it; any other is fetched (see fetchImage). Images
// are always hashed by content so the metadata hash pins what voters see, not just where it lives;
// videos are never passed here (only their URL is committed). An image that cannot be read or
// fetched is an ErrMediaUnavailable naming its url.
func (h *imageHasher) hashes(urls ...string) (map[string]string, error) {
	var hashes map[string]string
	for _, u := range urls {
		if u == "" {
			continue
		}
		sum, err := h.hash(u)
		if err != nil {
			return nil, errors.ErrMediaUnavailable.Withf("image %q: %v", u, err)
		}
		if hashes == nil {
			hashes = make(map[string]string)
		}
		hashes[u] = sum
	}
	return hashes, nil
}

// hash returns the hex SHA-256 of the content of one image url, reading it once.
func (h *imageHasher) hash(u string) (string, error) {
	if sum, ok := h.cache[u]; ok {
		return sum, nil
	}
	var data []byte
	if name, ok := h.a.objectStorage.LocalName(u); ok {
		object, err := h.a.objectStorage.GetByName(name)
		if err != nil {
			return "", err
		}
		data = object.Data
	} else {
		if h.client == nil {
			h.client = newMediaHTTPClient(mediaIPAllowed)
		}
		fetched, err := fetchImage(h.client, u)
		if err != nil {
			return "", err
		}
		data = fetched
	}
	sum := sha256.Sum256(data)
	h.cache[u] = hex.EncodeToString(sum[:])
	return h.cache[u], nil
}

// questionImageURLs returns the image urls of a question's choices (see account.ChoiceImageURLs),
// in choice order.
func questionImageURLs(q *db.VotingProcessQuestion) []string {
	_, choicesMeta := account.QuestionDisplayMeta(q.Metadata)
	var urls []string
	for _, ch := range q.Choices {
		urls = append(urls, account.ChoiceImageURLs(choicesMeta[ch.Value])...)
	}
	return urls
}

// publishMediaHashes hashes the images the election documents of a publish commit: the header for
// the parent election (unless it is already on chain) and each not yet published question's
// choice images, keyed by question id. A question without images has no entry.
func (a *API) publishMediaHashes(
	vp *db.VotingProcess, questions []db.VotingProcessQuestion,
) (parent map[string]string, byQuestion map[bson.ObjectID]map[string]string, err error) {
	h := a.newImageHasher()
	if len(vp.UpstreamID) == 0 {
		if parent, err = h.hashes(vp.Header); err != nil {
			return nil, nil, err
		}
	}
	for i := range questions {
		if len(questions[i].UpstreamID) > 0 {
			continue
		}
		hashes, err := h.hashes(questionImageURLs(&questions[i])...)
		if err != nil {
			return nil, nil, err
		}
		if hashes != nil {
			if byQuestion == nil {
				byQuestion = make(map[bson.ObjectID]map[string]string)
			}
			byQuestion[questions[i].ID] = hashes
		}
	}
	return parent, byQuestion, nil
}
