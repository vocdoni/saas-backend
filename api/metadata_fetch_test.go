package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	qt "github.com/frankban/quicktest"
)

// allowLoopbackMetadata lets fetchExternalMetadata reach httptest servers (which listen on
// loopback) for the duration of the test, restoring the public-only client afterwards.
func allowLoopbackMetadata(t *testing.T) {
	t.Helper()
	prev := metadataHTTPClient
	metadataHTTPClient = newMetadataHTTPClient(func(addr netip.Addr) bool {
		return addr.Unmap().IsLoopback() || isPublicIP(addr)
	})
	t.Cleanup(func() { metadataHTTPClient = prev })
}

func TestIsPublicIP(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"::ffff:8.8.8.8", true},
		{"127.0.0.1", false},
		{"127.10.20.30", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"10.0.0.1", false},
		{"172.16.5.4", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata service
		{"fe80::1", false},
		{"fd00:ec2::254", false}, // AWS IPv6 metadata service
		{"fc00::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"100.64.0.1", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"255.255.255.255", false},
		{"64:ff9b::7f00:1", false}, // NAT64 embedding 127.0.0.1
		{"2002:7f00:1::", false},   // 6to4 embedding 127.0.0.1
		{"198.18.0.1", false},
		{"192.0.2.1", false},
	} {
		c.Assert(isPublicIP(netip.MustParseAddr(tc.addr)), qt.Equals, tc.public, qt.Commentf("%s", tc.addr))
	}
	c.Assert(isPublicIP(netip.Addr{}), qt.IsFalse)
}

// TestFetchExternalMetadataRefusesNonPublic checks the default client never reaches a loopback
// server, either directly or through a redirect from an allowed host, and that only http(s)
// references are fetched at all.
func TestFetchExternalMetadataRefusesNonPublic(t *testing.T) {
	c := qt.New(t)
	var hits atomic.Int32
	internalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"title":"internal"}`))
	}))
	defer internalSrv.Close()

	// the production client refuses the loopback address after resolution.
	c.Assert(fetchExternalMetadata(t.Context(), internalSrv.URL), qt.IsNil)
	_, port, err := net.SplitHostPort(internalSrv.Listener.Addr().String())
	c.Assert(err, qt.IsNil)
	localhostURL := "http://localhost:" + port
	c.Assert(fetchExternalMetadata(t.Context(), localhostURL), qt.IsNil)
	c.Assert(hits.Load(), qt.Equals, int32(0))

	// non-http(s) schemes are never fetched.
	c.Assert(fetchExternalMetadata(t.Context(), "file:///etc/passwd"), qt.IsNil)
	c.Assert(fetchExternalMetadata(t.Context(), "ftp://127.0.0.1/metadata.json"), qt.IsNil)
	c.Assert(fetchExternalMetadata(t.Context(), "http:///no-host"), qt.IsNil)

	// redirects are not followed, even when the first hop is allowed.
	allowLoopbackMetadata(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internalSrv.URL, http.StatusFound)
	}))
	defer redirector.Close()
	c.Assert(fetchExternalMetadata(t.Context(), redirector.URL), qt.IsNil)
	c.Assert(hits.Load(), qt.Equals, int32(0))

	// sanity check: with loopback allowed the very same server is reachable.
	m := fetchExternalMetadata(t.Context(), internalSrv.URL)
	c.Assert(m, qt.Not(qt.IsNil))
	c.Assert(m["title"], qt.Equals, "internal")
	c.Assert(hits.Load(), qt.Equals, int32(1))
}
