package certauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
)

// testEnv is a certificate service on a loopback auth listener over a memStore.
type testEnv struct {
	s      *Service
	store  *memStore
	tokens *fakeTokens
	logs   *captureLog
	pki    *testprovider.PKI
	port   int
}

// newTestEnv starts a service with cfg adjusted by mutate, serving the auth listener on loopback.
func newTestEnv(c *qt.C, mutate func(*Config)) *testEnv {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, qt.IsNil)
	port := ln.Addr().(*net.TCPAddr).Port
	s, store, pki := newTestService(c, port, mutate)
	go func() { _ = s.Serve(ln) }()
	c.Cleanup(func() { _ = ln.Close() })
	tokens, ok := s.tokens.(*fakeTokens)
	c.Assert(ok, qt.IsTrue)
	logs, ok := s.log.(*captureLog)
	c.Assert(ok, qt.IsTrue)
	return &testEnv{s: s, store: store, tokens: tokens, logs: logs, pki: pki, port: port}
}

// attempt stores a fresh created attempt on the env's process, as the create endpoint does, and
// returns its state and auth URL.
func (e *testEnv) attempt(c *qt.C, returnURL string) (state, authURL string) {
	state, nonce, now := randomToken(), randomNonce(), time.Now()
	c.Assert(e.store.InsertCertAuthTransaction(&db.CertAuthTransaction{
		State: state, Nonce: nonce, Challenge: "challenge", ProcessID: e.store.vp.ID, ReturnURL: returnURL,
		Purpose: "vote", Status: db.CertAuthStatusCreated, CreatedAt: now, ExpiresAt: now.Add(stateTTL),
	}), qt.IsNil)
	return state, e.authURL(nonce, state)
}

// portString is the listener port as text.
func (e *testEnv) portString() string { return strconv.Itoa(e.port) }

// authURL is the auth listener URL of a nonce host and a state.
func (e *testEnv) authURL(nonce, state string) string {
	return "https://" + nonce + "." + testHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + state
}

// clientOptions shape the TLS client of the auth listener.
type clientOptions struct {
	cert       *tls.Certificate // presented when the server asks; nil presents none
	serverName string           // overrides the SNI
	onRequest  func(*tls.CertificateRequestInfo)
	dial       func(ctx context.Context, network, addr string) (net.Conn, error)
}

// client returns an HTTP client of the auth listener that never follows redirects and dials
// loopback whatever the URL host.
func (e *testEnv) client(o clientOptions) *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(e.pki.CA)
	dial := o.dial
	if dial == nil {
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(e.port))
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: dial, DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				RootCAs: roots, ServerName: o.serverName, MinVersion: tls.VersionTLS12,
				GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
					if o.onRequest != nil {
						o.onRequest(cri)
					}
					if o.cert == nil {
						return &tls.Certificate{}, nil
					}
					return o.cert, nil
				},
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

// authenticate GETs authURL with the certificate (nil: none) and returns the response.
func (e *testEnv) authenticate(c *qt.C, authURL string, cert *tls.Certificate) *http.Response {
	resp, err := e.client(clientOptions{cert: cert}).Get(authURL)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// fragment returns the target and the parsed fragment of a 303.
func fragment(c *qt.C, resp *http.Response) (string, url.Values) {
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
	loc, err := url.Parse(resp.Header.Get("Location"))
	c.Assert(err, qt.IsNil)
	values, err := url.ParseQuery(loc.Fragment)
	c.Assert(err, qt.IsNil)
	loc.Fragment = ""
	return loc.String(), values
}

// issue mints a synthetic certificate.
func (e *testEnv) issue(c *qt.C, h testprovider.Holder) *tls.Certificate {
	cert, err := e.pki.Issue(h)
	c.Assert(err, qt.IsNil)
	return cert
}
