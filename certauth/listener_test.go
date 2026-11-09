package certauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	proxyproto "github.com/pires/go-proxyproto"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
)

// TestCertAuthListener pins the auth listener (AC7): TLS 1.2, the providers' CAs in the
// CertificateRequest, ALPN http/1.1, no session resumption; the probe never asks for a certificate;
// a valid state answers the 303 with its code whether or not a certificate came; spent, expired,
// unknown and foreign-host states answer attempt_expired to the right return; unknown hosts 421.
func TestCertAuthListener(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	state, authURL := e.attempt(c, testReturnURL)
	nonceHost := e.store.transaction(state).Nonce + "." + testHost
	roots := x509.NewCertPool()
	roots.AddCert(e.pki.CA)
	addr := "127.0.0.1:" + strconv.Itoa(e.port)

	c.Run("handshake", func(c *qt.C) {
		var acceptable [][]byte
		cfg := &tls.Config{
			RootCAs: roots, ServerName: nonceHost, NextProtos: []string{"h2", "http/1.1"},
			ClientSessionCache: tls.NewLRUClientSessionCache(4), MinVersion: tls.VersionTLS12,
			GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
				acceptable = cri.AcceptableCAs
				return &tls.Certificate{}, nil
			},
		}
		for i := range 2 {
			conn, err := tls.Dial("tcp", addr, cfg)
			c.Assert(err, qt.IsNil)
			state := conn.ConnectionState()
			c.Assert(state.Version, qt.Equals, uint16(tls.VersionTLS12))
			c.Assert(state.NegotiatedProtocol, qt.Equals, "http/1.1")
			// session tickets are off: the second connection is a full handshake, picker included
			c.Assert(state.DidResume, qt.IsFalse, qt.Commentf("connection %d", i))
			c.Assert(conn.Close(), qt.IsNil)
		}
		var want []string
		for _, p := range e.s.handler.Providers() {
			for _, ca := range p.CAs() {
				want = append(want, string(ca.RawSubject))
			}
		}
		got := make([]string, 0, len(acceptable))
		for _, name := range acceptable {
			got = append(got, string(name))
		}
		slices.Sort(want)
		slices.Sort(got)
		c.Assert(got, qt.DeepEquals, slices.Compact(want))
		c.Assert(slices.Contains(got, string(e.pki.CA.RawSubject)), qt.IsTrue)

		// the probe host never sends a CertificateRequest
		asked := false
		probeCfg := cfg.Clone()
		probeCfg.ServerName = "probe." + testHost
		probeCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			asked = true
			return &tls.Certificate{}, nil
		}
		conn, err := tls.Dial("tcp", addr, probeCfg)
		c.Assert(err, qt.IsNil)
		c.Assert(conn.Close(), qt.IsNil)
		c.Assert(asked, qt.IsFalse)

		// any other name fails the handshake
		other := cfg.Clone()
		other.ServerName = "www." + testHost
		_, err = tls.Dial("tcp", addr, other)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("authenticate", func(c *qt.C) {
		cert := e.issue(c, testprovider.Holder{GivenName: "Maria", Surname: "García", Document: "12345678Z"})
		for _, presented := range []*tls.Certificate{cert, nil} {
			state, authURL := e.attempt(c, testReturnURL)
			resp := e.authenticate(c, authURL, presented)
			target, frag := fragment(c, resp)
			c.Assert(target, qt.Equals, testReturnURL)
			c.Assert(frag.Get("state"), qt.Equals, state)
			c.Assert(frag.Get("code"), qt.Matches, `[A-Za-z0-9_-]{43}`)
			c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "no-store")
			c.Assert(resp.Header.Get("Referrer-Policy"), qt.Equals, "no-referrer")
			c.Assert(resp.Close, qt.IsTrue) // Connection: close
			body, err := io.ReadAll(resp.Body)
			c.Assert(err, qt.IsNil)
			c.Assert(body, qt.HasLen, 0)
			tx := e.store.transaction(state)
			c.Assert(tx.Status, qt.Equals, db.CertAuthStatusAuthenticated)
			c.Assert(tx.ChainPresented, qt.Equals, presented != nil)
			if presented != nil {
				c.Assert(tx.Chain, qt.DeepEquals, [][]byte{cert.Certificate[0]})
			}

			// the same state again is spent: back to its own return URL
			target, frag = fragment(c, e.authenticate(c, authURL, presented))
			c.Assert(target, qt.Equals, testReturnURL)
			c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")
		}

		// an expired state goes back to its own return URL too
		old := &db.CertAuthTransaction{
			State: randomToken(), Nonce: randomNonce(), Challenge: "c", ProcessID: e.store.vp.ID,
			ReturnURL: testOrigin + "/es", Purpose: "vote", Status: db.CertAuthStatusCreated,
			CreatedAt: time.Now().Add(-16 * time.Minute),
		}
		c.Assert(e.store.InsertCertAuthTransaction(old), qt.IsNil)
		oldURL := "https://" + old.Nonce + "." + testHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + old.State
		target, frag := fragment(c, e.authenticate(c, oldURL, nil))
		c.Assert(target, qt.Equals, testOrigin+"/es")
		c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")

		// an unknown state, a malformed one, and a state replayed on another nonce host are unknown:
		// the default return
		state, _ := e.attempt(c, testReturnURL)
		for _, u := range []string{
			"https://" + nonceHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + randomToken(),
			"https://" + nonceHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=short",
			e.authURL(randomNonce(), state),
		} {
			target, frag := fragment(c, e.authenticate(c, u, nil))
			c.Assert(target, qt.Equals, testDefault, qt.Commentf("%s", u))
			c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")
		}
		// the replay on another host did not spend the state
		c.Assert(e.store.transaction(state).Status, qt.Equals, db.CertAuthStatusCreated)
	})

	c.Run("probe", func(c *qt.C) {
		probeOK := `{"ok":true}`
		probe := "https://probe." + testHost + ":" + strconv.Itoa(e.port) + probePath
		for origin, allowed := range map[string]bool{testOrigin: true, "https://evil.test": false} {
			req, err := http.NewRequest(http.MethodGet, probe, nil)
			c.Assert(err, qt.IsNil)
			req.Header.Set("Origin", origin)
			resp, err := e.client(clientOptions{}).Do(req)
			c.Assert(err, qt.IsNil)
			body, err := io.ReadAll(resp.Body)
			c.Assert(err, qt.IsNil)
			c.Assert(resp.Body.Close(), qt.IsNil)
			c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
			c.Assert(string(body), qt.Equals, probeOK)
			c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "no-store")
			want := ""
			if allowed {
				want = origin
			}
			c.Assert(resp.Header.Get("Access-Control-Allow-Origin"), qt.Equals, want)
			c.Assert(resp.Header.Get("Access-Control-Allow-Credentials"), qt.Equals, "")
		}
	})

	c.Run("misdirected", func(c *qt.C) {
		// a Host that names nothing the listener serves, or another host than the SNI, is 421
		for _, host := range []string{"evil.test", randomNonce() + "." + testHost} {
			req, err := http.NewRequest(http.MethodGet, authURL, nil)
			c.Assert(err, qt.IsNil)
			req.Host = host
			resp, err := e.client(clientOptions{serverName: nonceHost}).Do(req)
			c.Assert(err, qt.IsNil)
			c.Assert(resp.Body.Close(), qt.IsNil)
			c.Assert(resp.StatusCode, qt.Equals, http.StatusMisdirectedRequest, qt.Commentf("host %s", host))
		}
	})
}

// TestBoundChain pins the storage bounds of a presented chain: within 10 certificates and 200 KiB
// it is kept, beyond them the leaf alone when it is within cert-check's size bound, else nothing.
func TestBoundChain(t *testing.T) {
	c := qt.New(t)
	leaf, small, big := []byte("leaf"), make([]byte, 1024), make([]byte, 17<<10)
	c.Assert(boundChain(nil), qt.IsNil)
	c.Assert(boundChain([][]byte{leaf, small}), qt.DeepEquals, [][]byte{leaf, small})
	long := [][]byte{leaf}
	for range 10 {
		long = append(long, small)
	}
	c.Assert(boundChain(long), qt.DeepEquals, [][]byte{leaf})
	heavy := [][]byte{leaf}
	for range 9 {
		heavy = append(heavy, make([]byte, 25<<10))
	}
	c.Assert(boundChain(heavy), qt.DeepEquals, [][]byte{leaf})
	c.Assert(boundChain(append([][]byte{big}, long[1:]...)), qt.IsNil)
}

// TestCertAuthStartupValidationProxyProtocol pins the PROXY protocol front (AC16): from a front
// CIDR the header's source is the client IP the logs record; from anyone else the connection is
// plain TCP, so a header fails the handshake and nobody else chooses the logged IP.
func TestCertAuthStartupValidationProxyProtocol(t *testing.T) {
	c := qt.New(t)
	conveyed := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5555}
	withProxyHeader := func(e *testEnv) func(ctx context.Context, network, addr string) (net.Conn, error) {
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(e.port))
			if err != nil {
				return nil, err
			}
			header := &proxyproto.Header{
				Version: 2, Command: proxyproto.PROXY, TransportProtocol: proxyproto.TCPv4,
				SourceAddr: conveyed, DestinationAddr: conn.RemoteAddr(),
			}
			if _, err := header.WriteTo(conn); err != nil {
				return nil, err
			}
			return conn, nil
		}
	}
	lastClientIP := func(e *testEnv) any {
		var ip any
		for _, entry := range e.logs.all() {
			if entry.msg == "certificate attempt authenticated" {
				ip = entry.kv["clientIP"]
			}
		}
		return ip
	}

	// from a front CIDR the header is read and its source is the client IP the logs record
	front := newTestEnv(c, func(cfg *Config) {
		cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"127.0.0.0/8"}
	})
	_, authURL := front.attempt(c, testReturnURL)
	resp, err := front.client(clientOptions{dial: withProxyHeader(front)}).Get(authURL)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.Body.Close(), qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
	c.Assert(lastClientIP(front), qt.Equals, "203.0.113.7")
	// a plain connection from the front still works
	_, authURL = front.attempt(c, testReturnURL)
	_, frag := fragment(c, front.authenticate(c, authURL, nil))
	c.Assert(frag.Get("code"), qt.Not(qt.Equals), "")

	// from anywhere else the header is not read: it is plain TCP, so the handshake fails and
	// nobody else can choose the logged IP
	other := newTestEnv(c, func(cfg *Config) {
		cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/8"}
	})
	_, authURL = other.attempt(c, testReturnURL)
	_, err = other.client(clientOptions{dial: withProxyHeader(other)}).Get(authURL)
	c.Assert(err, qt.Not(qt.IsNil))
	_, authURL = other.attempt(c, testReturnURL)
	_, frag = fragment(c, other.authenticate(c, authURL, nil))
	c.Assert(frag.Get("code"), qt.Not(qt.Equals), "")
	c.Assert(lastClientIP(other), qt.Equals, "127.0.0.1")
}
