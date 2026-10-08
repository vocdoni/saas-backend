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

	"github.com/vocdoni/saas-backend/errors"
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

// imageHashes returns the lowercase hex SHA-256 of the content of each image url, keyed by url
// exactly as given; empty urls are skipped and nil is returned when there is none. An image this
// backend's object storage serves is read from it; any other is fetched (see fetchImage). Images
// are always hashed by content so the metadata hash pins what voters see, not just where it lives;
// videos are never passed here (only their URL is committed). An image that cannot be read or
// fetched is an ErrMediaUnavailable naming its url.
func (a *API) imageHashes(urls ...string) (map[string]string, error) {
	var hashes map[string]string
	var client *http.Client
	for _, u := range urls {
		if u == "" {
			continue
		}
		var data []byte
		if name, ok := a.objectStorage.LocalName(u); ok {
			object, err := a.objectStorage.GetByName(name)
			if err != nil {
				return nil, errors.ErrMediaUnavailable.Withf("image %q: %v", u, err)
			}
			data = object.Data
		} else {
			if client == nil {
				client = newMediaHTTPClient(mediaIPAllowed)
			}
			fetched, err := fetchImage(client, u)
			if err != nil {
				return nil, errors.ErrMediaUnavailable.Withf("image %q: %v", u, err)
			}
			data = fetched
		}
		if hashes == nil {
			hashes = make(map[string]string)
		}
		sum := sha256.Sum256(data)
		hashes[u] = hex.EncodeToString(sum[:])
	}
	return hashes, nil
}
