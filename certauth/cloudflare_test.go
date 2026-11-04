package certauth

import (
	"encoding/base64"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
)

// TestCertAuthCloudflareMode pins cloudflare mode (AC14): only a peer presenting the Authenticated
// Origin Pulls certificate completes a handshake, only allowlisted peers connect when the list is
// set, and the forwarded RFC 9440 and legacy headers rebuild the chain the origin mode stores.
func TestCertAuthCloudflareMode(t *testing.T) {
	c := qt.New(t)
	aop, err := testprovider.NewPKI() // stands for Cloudflare's origin pull CA
	c.Assert(err, qt.IsNil)
	aopCert, err := aop.Issue(testprovider.Holder{Document: "00000000T"})
	c.Assert(err, qt.IsNil)
	cloudflare := func(cidrs ...string) func(*Config) {
		return func(cfg *Config) {
			cfg.Mode, cfg.CFOriginPullCAPEM, cfg.CFAllowedCIDRs = ModeCloudflare, aop.CertPEM(), cidrs
		}
	}
	e := newTestEnv(c, cloudflare())
	leaf := e.issue(c, testprovider.Holder{GivenName: "Maria", Surname: "García", Document: "12345678Z"})
	intermediate := e.pki.CA.Raw
	b64 := base64.StdEncoding.EncodeToString

	forward := func(env *testEnv, headers map[string]string) (*http.Response, string, error) {
		state, authURL := env.attempt(c, testReturnURL)
		req, err := http.NewRequest(http.MethodGet, authURL, nil)
		c.Assert(err, qt.IsNil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := env.client(clientOptions{cert: aopCert}).Do(req)
		if err == nil {
			c.Assert(resp.Body.Close(), qt.IsNil)
		}
		return resp, state, err
	}

	for name, tc := range map[string]struct {
		headers   map[string]string
		chain     [][]byte
		presented bool
	}{
		"rfc 9440": {
			headers: map[string]string{
				"Client-Cert":       ":" + b64(leaf.Certificate[0]) + ":",
				"Client-Cert-Chain": ":" + b64(intermediate) + ":, :" + b64(intermediate) + ":",
			},
			chain: [][]byte{leaf.Certificate[0], intermediate, intermediate}, presented: true,
		},
		"rfc 9440 leaf only": {
			headers: map[string]string{"Client-Cert": ":" + b64(leaf.Certificate[0]) + ":"},
			chain:   [][]byte{leaf.Certificate[0]}, presented: true,
		},
		"legacy": {
			headers: map[string]string{"Cf-Client-Cert-Der-Base64": b64(leaf.Certificate[0])},
			chain:   [][]byte{leaf.Certificate[0]}, presented: true,
		},
		"no certificate":     {headers: map[string]string{}},
		"undecodable":        {headers: map[string]string{"Client-Cert": "not a byte sequence"}, presented: true},
		"chain without leaf": {headers: map[string]string{"Client-Cert-Chain": ":" + b64(intermediate) + ":"}},
	} {
		resp, state, err := forward(e, tc.headers)
		c.Assert(err, qt.IsNil, qt.Commentf("%s", name))
		c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther, qt.Commentf("%s", name))
		tx := e.store.transaction(state)
		c.Assert(tx.ChainPresented, qt.Equals, tc.presented, qt.Commentf("%s", name))
		c.Assert(tx.Chain, qt.HasLen, len(tc.chain), qt.Commentf("%s", name))
		for i := range tc.chain {
			c.Assert(tx.Chain[i], qt.DeepEquals, tc.chain[i], qt.Commentf("%s: certificate %d", name, i))
		}
	}

	// the leaf the headers carry is stored exactly as origin mode stores the TLS peer certificate
	origin := newTestEnv(c, nil)
	state, authURL := origin.attempt(c, testReturnURL)
	origin.authenticate(c, authURL, leaf)
	c.Assert(origin.store.transaction(state).Chain, qt.DeepEquals, [][]byte{leaf.Certificate[0]})

	// without the origin pull certificate the handshake fails, so a forged header never arrives
	_, authURL = e.attempt(c, testReturnURL)
	req, err := http.NewRequest(http.MethodGet, authURL, nil)
	c.Assert(err, qt.IsNil)
	req.Header.Set("Client-Cert", ":"+b64(leaf.Certificate[0])+":")
	_, err = e.client(clientOptions{cert: leaf}).Do(req)
	c.Assert(err, qt.Not(qt.IsNil))
	_, err = e.client(clientOptions{}).Do(req)
	c.Assert(err, qt.Not(qt.IsNil))

	// with a peer allowlist, connections from elsewhere are dropped at accept
	_, _, err = forward(newTestEnv(c, cloudflare("10.0.0.0/8")), map[string]string{})
	c.Assert(err, qt.Not(qt.IsNil))
	resp, _, err := forward(newTestEnv(c, cloudflare("127.0.0.0/8")), map[string]string{})
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
}
