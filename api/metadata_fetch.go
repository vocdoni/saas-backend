package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/vocdoni/saas-backend/internal"
)

const (
	// metadataFetchTimeout bounds a whole external metadata fetch: dial, TLS, headers and body.
	metadataFetchTimeout = 3 * time.Second
	// metadataMaxBytes caps how much of an external metadata document is read.
	metadataMaxBytes = internal.MiB
)

// nonPublicPrefixes lists the special-purpose ranges not covered by the netip.Addr predicates
// used in isPublicIP, that an outbound fetch must never reach.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this" network
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, can embed any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments (Teredo, ORCHID...)
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, can embed any IPv4 address
}

// isPublicIP reports whether addr is a globally routable unicast address, i.e. not loopback,
// private, link-local (which includes the 169.254.169.254 cloud metadata service), multicast,
// unspecified or any other special-purpose range.
func isPublicIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// newMetadataHTTPClient returns the client used to fetch external metadata documents. Every
// connection is checked against allowed after DNS resolution, right before the socket connects,
// so a hostname cannot resolve (or re-resolve, as in DNS rebinding) to a forbidden address.
// Redirects are not followed and no proxy is used, so the checked address is the one contacted.
func newMetadataHTTPClient(allowed func(netip.Addr) bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: metadataFetchTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			addrPort, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("parsing dialed address %q: %w", address, err)
			}
			if !allowed(addrPort.Addr()) {
				return fmt.Errorf("refusing to connect to non-public address %s", addrPort.Addr())
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: metadataFetchTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   metadataFetchTimeout,
			ResponseHeaderTimeout: metadataFetchTimeout,
			MaxIdleConns:          16,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// hand the 3xx back as is; fetchExternalMetadata only accepts a 200.
			return http.ErrUseLastResponse
		},
	}
}

// metadataHTTPClient fetches external legacy election metadata, restricted to public addresses.
// Tests swap it to reach httptest servers on loopback.
var metadataHTTPClient = newMetadataHTTPClient(isPublicIP)
