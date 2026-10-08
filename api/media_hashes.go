package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	// mediaFetchTimeout bounds the whole fetch of one external image, redirects included.
	mediaFetchTimeout = 10 * time.Second
	// mediaMaxBytes caps the size of an imported image: the same bound the upload handler parses
	// its multipart forms with.
	mediaMaxBytes = 32 << 20
	// mediaMaxRedirects caps how many redirects an external image fetch follows.
	mediaMaxRedirects = 3
	// mediaImportWorkers bounds how many images one request imports at once.
	mediaImportWorkers = 4
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

// fetchImage downloads an external image: an http(s) URL answering 200 with at most
// mediaMaxBytes of body. Whether the body is an image is decided by the object storage on import.
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
		return nil, fmt.Errorf("not reachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("not reachable: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("not reachable: reading body: %w", err)
	}
	if len(data) > mediaMaxBytes {
		return nil, fmt.Errorf("too large: over %d bytes", mediaMaxBytes)
	}
	return data, nil
}

// imageRef is one image URL a process carries, named by the request field it came from, with the
// setter that repoints that field at another URL.
type imageRef struct {
	field string
	url   string
	set   func(string)
}

// processImageRefs returns every image URL a process request carries: its header and each
// question's choice images (metadata.choices[].image, a URL or a {default, thumbnail} object).
func processImageRefs(header *string, questions []apicommon.VotingProcessQuestionRequest) []imageRef {
	var refs []imageRef
	if *header != "" {
		refs = append(refs, imageRef{field: "header", url: *header, set: func(u string) { *header = u }})
	}
	for i := range questions {
		prefix := fmt.Sprintf("questions[%d]", i)
		refs = append(refs, questionImageRefs(prefix, questions[i].Metadata, questions[i].Choices)...)
	}
	return refs
}

// questionImageRefs returns the choice image URLs of one question's metadata, prefixing each
// field name with prefix. A choice image is named after the choice it belongs to, matched by value.
func questionImageRefs(prefix string, metadata map[string]any, choices []db.Choice) []imageRef {
	var refs []imageRef
	for j, entry := range account.ChoicesMetaEntries(metadata) {
		field := fmt.Sprintf("%s.metadata.choices[%d].image", prefix, j)
		if value, ok := account.ChoiceMetaValue(entry); ok {
			for k := range choices {
				if choices[k].Value == value {
					field = fmt.Sprintf("%s.choices[%d].image", prefix, k)
					break
				}
			}
		}
		refs = append(refs, choiceImageRefs(field, entry)...)
	}
	return refs
}

// choiceImageRefs returns the image URLs of one choice's display info entry: its "image" as a
// plain URL, or the "default" and "thumbnail" URLs of an image object.
func choiceImageRefs(field string, entry map[string]any) []imageRef {
	if image, ok := entry["image"].(string); ok {
		if image == "" {
			return nil
		}
		return []imageRef{{field: field, url: image, set: func(u string) { entry["image"] = u }}}
	}
	image, ok := account.AsMap(entry["image"])
	if !ok {
		return nil
	}
	var refs []imageRef
	for _, key := range []string{"default", "thumbnail"} {
		if u, ok := image[key].(string); ok && u != "" {
			refs = append(refs, imageRef{field: field + "." + key, url: u, set: func(u string) { image[key] = u }})
		}
	}
	return refs
}

// importImages copies every external image among refs into this backend's object storage and
// repoints its field at the local copy, so what is stored, published and hashed never depends on
// a remote server again. Images already served by the object storage are left as they are. The
// fetches run concurrently (at most mediaImportWorkers at once) and the fields are repointed only
// once all of them succeeded: on a failure nothing is changed and the error, an
// ErrMediaUnavailable, names the field and the URL. The same URL is fetched once.
func (a *API) importImages(refs []imageRef, owner string) error {
	local := make(map[string]string)
	var external []string
	for _, ref := range refs {
		if _, ok := a.objectStorage.LocalName(ref.url); ok {
			continue
		}
		if _, seen := local[ref.url]; !seen {
			local[ref.url] = ""
			external = append(external, ref.url)
		}
	}
	if len(external) == 0 {
		return nil
	}
	client := newMediaHTTPClient(mediaIPAllowed)
	errs := make([]error, len(external))
	imported := make([]string, len(external))
	sem := make(chan struct{}, mediaImportWorkers)
	var wg sync.WaitGroup
	for i, u := range external {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			imported[i], errs[i] = a.importImage(client, u, owner)
		})
	}
	wg.Wait()
	for _, ref := range refs {
		for i, u := range external {
			if u == ref.url && errs[i] != nil {
				return errors.ErrMediaUnavailable.Withf("%s: %s: %v", ref.field, u, errs[i])
			}
		}
	}
	for i, u := range external {
		local[u] = imported[i]
	}
	for _, ref := range refs {
		if u, ok := local[ref.url]; ok {
			ref.set(u)
		}
	}
	return nil
}

// importImage fetches one external image and stores it in the object storage, which accepts only
// the image types it supports, returning the local URL of the copy.
func (a *API) importImage(client *http.Client, u, owner string) (string, error) {
	data, err := fetchImage(client, u)
	if err != nil {
		return "", err
	}
	name, err := a.objectStorage.Put(bytes.NewReader(data), int64(len(data)), owner)
	if err != nil {
		return "", fmt.Errorf("not an image: %w", err)
	}
	return a.objectStorage.LocalURL(name), nil
}

// imageHasher hashes the images of election metadata documents by content, from the bytes this
// backend's object storage holds (external images are imported when a process is saved, so
// building a document never touches the network). Each url is read once however many documents or
// choices name it.
type imageHasher struct {
	a     *API
	cache map[string]string
}

// newImageHasher returns an imageHasher with an empty cache.
func (a *API) newImageHasher() *imageHasher {
	return &imageHasher{a: a, cache: make(map[string]string)}
}

// hashes returns the lowercase hex SHA-256 of the content of each image url, keyed by url exactly
// as given; empty urls are skipped and nil is returned when there is none. Videos are never passed
// here (only their URL is committed). A url that is not a readable stored object is an
// ErrMediaUnavailable naming it.
func (h *imageHasher) hashes(urls ...string) (map[string]string, error) {
	var hashes map[string]string
	for _, u := range urls {
		if u == "" {
			continue
		}
		sum, err := h.hash(u)
		if err != nil {
			return nil, errors.ErrMediaUnavailable.Withf("image %s: %v", u, err)
		}
		if hashes == nil {
			hashes = make(map[string]string)
		}
		hashes[u] = sum
	}
	return hashes, nil
}

// hash returns the hex SHA-256 of the stored content of one image url, reading it once.
func (h *imageHasher) hash(u string) (string, error) {
	if sum, ok := h.cache[u]; ok {
		return sum, nil
	}
	name, ok := h.a.objectStorage.LocalName(u)
	if !ok {
		return "", fmt.Errorf("not stored by this backend; save the process again to import it")
	}
	object, err := h.a.objectStorage.GetByName(name)
	if err != nil {
		return "", fmt.Errorf("stored object not readable: %w", err)
	}
	sum := sha256.Sum256(object.Data)
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
