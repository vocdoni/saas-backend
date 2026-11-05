package certauth

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
)

// TestCertAuthStartupValidation pins the startup rules (spec section 1, AC16): every misconfiguration
// refuses to start, and the PROXY protocol conveys the client IP only from the front CIDRs.
func TestCertAuthStartupValidation(t *testing.T) {
	c := qt.New(t)
	pki, err := testprovider.NewPKI()
	c.Assert(err, qt.IsNil)
	deps := Deps{Storage: newMemStore(), Tokens: &fakeTokens{}}
	otherCert, otherKey, err := pki.ServerCertificate("*.elsewhere.test")
	c.Assert(err, qt.IsNil)
	nonceOnly, nonceOnlyKey, err := pki.ServerCertificate(randomNonce() + "." + testHost)
	c.Assert(err, qt.IsNil)

	_, err = New(testConfig(c, pki, 443), deps)
	c.Assert(err, qt.IsNil)

	for name, mutate := range map[string]func(*Config){
		"unknown mode":                     func(cfg *Config) { cfg.Mode = "passthrough" },
		"host with port":                   func(cfg *Config) { cfg.Host = "auth.test:8443" },
		"upper case host":                  func(cfg *Config) { cfg.Host = "Auth.test" },
		"certificate for another domain":   func(cfg *Config) { cfg.TLSCertPEM, cfg.TLSKeyPEM = otherCert, otherKey },
		"certificate without the wildcard": func(cfg *Config) { cfg.TLSCertPEM, cfg.TLSKeyPEM = nonceOnly, nonceOnlyKey },
		"no certificate":                   func(cfg *Config) { cfg.TLSCertPEM = nil },
		"no allowed origin":                func(cfg *Config) { cfg.AllowedOrigins = nil },
		"http origin":                      func(cfg *Config) { cfg.AllowedOrigins = []string{"http://app.test"} },
		"origin with a path":               func(cfg *Config) { cfg.AllowedOrigins = []string{"https://app.test/"} },
		"upper case origin":                func(cfg *Config) { cfg.AllowedOrigins = []string{"https://App.test"} },
		"default return elsewhere":         func(cfg *Config) { cfg.DefaultReturn = "https://evil.test/" },
		"unknown provider":                 func(cfg *Config) { cfg.Providers = []string{"fnmt", "dnie"} },
		"representative provider":          func(cfg *Config) { cfg.Providers = []string{"fnmt-representative"} },
		"no provider":                      func(cfg *Config) { cfg.Providers, cfg.TestProvider = nil, false },
		"proxy protocol without fronts":    func(cfg *Config) { cfg.ProxyProtocol = true },
		"proxy protocol bad cidr": func(cfg *Config) {
			cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/33"}
		},
		"cloudflare without origin pull CA": func(cfg *Config) { cfg.Mode, cfg.CFAcknowledged = ModeCloudflare, true },
		"proxy protocol in cloudflare mode": func(cfg *Config) {
			cfg.Mode, cfg.CFOriginPullCAPEM, cfg.CFAcknowledged = ModeCloudflare, pki.CertPEM(), true
			cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/8"}
		},
		"test provider against production": func(cfg *Config) { cfg.VocdoniAPI = "https://api.vocdoni.io/v2" },
		"test provider against stage": func(cfg *Config) {
			cfg.VocdoniAPI = "https://api-stg.vocdoni.net/v2"
		},
		"test provider against an unknown": func(cfg *Config) { cfg.VocdoniAPI = "https://api-lts.example.org/v2" },
		"test provider without CA":         func(cfg *Config) { cfg.TestProviderCAPEM = nil },
		"no root key":                      func(cfg *Config) { cfg.RootKey = nil },
	} {
		cfg := testConfig(c, pki, 443)
		mutate(&cfg)
		_, err := New(cfg, deps)
		c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("%s", name))
	}

	// accepted variations: localhost dev origins, an extra allowlisted test API, cloudflare with its CA;
	// stage requires an explicit --certAuthTestProviderAllowedAPIs entry
	for name, mutate := range map[string]func(*Config){
		"localhost origin": func(cfg *Config) { cfg.AllowedOrigins = append(cfg.AllowedOrigins, "http://127.0.0.1:5173") },
		"extra test api": func(cfg *Config) {
			cfg.VocdoniAPI, cfg.TestProviderAllowedAPIs = "https://api-e2e.internal/v2", []string{"api-e2e.internal"}
		},
		"dev api": func(cfg *Config) { cfg.VocdoniAPI = "https://api-dev.vocdoni.net/v2" },
		"stage api explicit opt-in": func(cfg *Config) {
			cfg.VocdoniAPI, cfg.TestProviderAllowedAPIs = "https://api-stg.vocdoni.net/v2", []string{"api-stg.vocdoni.net"}
		},
		"cloudflare": func(cfg *Config) {
			cfg.Mode, cfg.CFOriginPullCAPEM, cfg.CFAcknowledged = ModeCloudflare, pki.CertPEM(), true
		},
	} {
		cfg := testConfig(c, pki, 443)
		mutate(&cfg)
		_, err := New(cfg, deps)
		c.Assert(err, qt.IsNil, qt.Commentf("%s", name))
	}

	// cloudflare mode is an explicit opt-in: without the acknowledgement it refuses to start, even
	// with its CA, and says why; with it, it starts and still warns
	cfg := testConfig(c, pki, 443)
	cfg.Mode, cfg.CFOriginPullCAPEM = ModeCloudflare, pki.CertPEM()
	_, err = New(cfg, deps)
	c.Assert(err, qt.ErrorIs, errCloudflareNotAcknowledged)
	c.Assert(err, qt.ErrorMatches, `.*certAuthCloudflareAcknowledged.*per-zone or per-hostname.*`)
	cfg.CFAcknowledged = true
	s, err := New(cfg, deps)
	c.Assert(err, qt.IsNil)
	c.Assert(s.v.warnings, qt.Any(qt.Matches), `cloudflare mode is not production ready.*`)
}
