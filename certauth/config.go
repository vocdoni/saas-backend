// Package certauth signs voters in with a qualified digital certificate (FNMT natural person,
// idCAT) instead of typed member details. It owns the second, TLS listener the browser presents its
// certificate to, the per-attempt transaction flow (create, authenticate, redeem) of
// contract-cert-auth.md, and the error envelope of its failures. Verification is cert-check's; the
// match against the census is SP1's canonical nationalId login hash. A nil *Service means the
// deployment runs without the feature.
package certauth

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/cert-check/providers/fnmt"
	"github.com/vocdoni/cert-check/providers/idcat"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp"
)

// Modes of the auth listener, selected by --certAuthMode.
const (
	// ModeOrigin terminates TLS in this process with cert-check's TLS configuration.
	ModeOrigin = "origin"
	// ModeCloudflare trusts Cloudflare to terminate the browser's mTLS and forward the certificate
	// in headers. Not for production until its open blockers are verified (spec section 3b).
	ModeCloudflare = "cloudflare"
)

// Defaults of the configuration.
const (
	// DefaultListenAddr is the bind address of the auth listener.
	DefaultListenAddr = ":8443"
	// DefaultPublicPort is the port written into authUrl and probeUrl (omitted when 443).
	DefaultPublicPort = 443
	// DefaultCreatesPerMinute caps the attempts created per client IP and process per minute.
	DefaultCreatesPerMinute = 60
)

// Hosts, paths and protocol of the auth listener.
const (
	// nonceLen is the length of a nonce host label.
	nonceLen = 26
	// probeLabel is the first label of the probe host.
	probeLabel = "probe"
	// authenticatePath is the path of the authenticate endpoint on a nonce host.
	authenticatePath = "/v1/authenticate"
	// probePath is the path of the probe endpoint on the probe host.
	probePath = "/ok"
	// alpnHTTP1 is the only ALPN protocol the auth listener speaks: no HTTP/2 coalescing across hosts.
	alpnHTTP1 = "http/1.1"
)

// Lifetimes of an attempt (contract sections 3 and 4).
const (
	stateTTL = 15 * time.Minute
	codeTTL  = 5 * time.Minute
	// verifyTimeout bounds Handler.Verify (revocation): the main listener times out at 45 s and the
	// app waits 45 s.
	verifyTimeout = 40 * time.Second //nolint:unused
)

// testProviderAPIHosts are the --vocdoniApi hosts the synthetic test provider may run against
// besides --certAuthTestProviderAllowedAPIs: an allowlist, so an unknown API fails closed.
var testProviderAPIHosts = []string{"localhost", "127.0.0.1", "api-dev.vocdoni.net", "api-stg.vocdoni.net"}

// providerConstructors are the cert-check providers a deployment may enable: natural persons only
// (owner decisions 1 and 4). The representative and employee constructors are never referenced.
var providerConstructors = map[string]func(...certcheck.Option) certcheck.Provider{
	"fnmt":  fnmt.New,
	"idcat": idcat.New,
}

// Config is the certificate sign-in configuration (the --certAuth* flags). PEM material is passed
// as bytes; cmd/service reads the files.
type Config struct {
	// Mode is ModeOrigin or ModeCloudflare.
	Mode string
	// Host is the auth domain suffix: attempts run on <nonce>.<Host>, the probe on probe.<Host>.
	Host string
	// ListenAddr is the bind address of the auth listener.
	ListenAddr string
	// PublicPort is the port of authUrl and probeUrl, omitted from them when 443.
	PublicPort int
	// TLSCertPEM and TLSKeyPEM are the auth listener's server certificate, covering *.<Host>.
	TLSCertPEM []byte
	TLSKeyPEM  []byte
	// Providers are the cert-check provider IDs to enable ("fnmt", "idcat").
	Providers []string
	// AllowedOrigins are the exact app origins a returnUrl, the CORS of the two JSON endpoints and
	// the probe accept. https, or http for localhost and 127.0.0.1 only.
	AllowedOrigins []string
	// DefaultReturn is where an unknown attempt is sent back to (an allowlisted origin's URL).
	DefaultReturn string
	// CreatesPerMinute caps the attempts created per client IP and process per minute; <= 0 disables it.
	CreatesPerMinute int
	// MaxHourlyFailures caps the redeems per presented certificate, and per verified document, on a
	// process per hour. 0 uses the CSP's default; a negative value disables the cap.
	MaxHourlyFailures int
	// TrustForwardedFor takes the client IP from the rightmost X-Forwarded-For entry: set it when the
	// main listener is only reachable through the trusted edge proxy (api.Config.OriginSecret).
	TrustForwardedFor bool
	// ProxyProtocol accepts PROXY protocol headers on the auth listener (origin mode), only from
	// the ProxyFrontCIDRs.
	ProxyProtocol   bool
	ProxyFrontCIDRs []string
	// CFOriginPullCAPEM is the CA of Cloudflare's Authenticated Origin Pulls client certificate
	// (cloudflare mode, required).
	CFOriginPullCAPEM []byte
	// CFAllowedCIDRs, when set, are the only peers the auth listener accepts in cloudflare mode.
	CFAllowedCIDRs []string
	// CFAcknowledged is the explicit opt-in cloudflare mode refuses to start without (Task 12's
	// --certAuthCloudflareAcknowledged): set only after the blockers in the spec section 3b are verified
	// and the AOP CA is a per-zone or per-hostname certificate, never Cloudflare's global one.
	CFAcknowledged bool
	// TestProvider adds the synthetic provider of TestProviderCAPEM, only against an allowlisted API.
	TestProvider            bool
	TestProviderCAPEM       []byte
	TestProviderAllowedAPIs []string
	// VocdoniAPI is the --vocdoniApi URL, checked against the test provider allowlist.
	VocdoniAPI string
	// RootKey is the service root key, from which the voterRef secret derives.
	RootKey []byte
}

// validated is what New derives from a valid Config.
type validated struct {
	serverCert *tls.Certificate
	origins    map[string]bool
	proxyNets  []*net.IPNet
	cfNets     []*net.IPNet
	cfCAs      *x509.CertPool
	providers  []certcheck.Provider
	maxFails   int
	warnings   []string
}

// validate checks every rule of spec section 1, failing on the first broken one.
func (cfg *Config) validate() (*validated, error) {
	v := &validated{}
	switch cfg.Mode {
	case ModeOrigin, ModeCloudflare:
	default:
		return nil, fmt.Errorf("certauth: unknown mode %q (origin or cloudflare)", cfg.Mode)
	}
	host := strings.ToLower(cfg.Host)
	if host != cfg.Host || !strings.Contains(host, ".") || strings.ContainsAny(host, ":/ *") {
		return nil, fmt.Errorf("certauth: host %q must be a lower case domain name without port", cfg.Host)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	if cfg.PublicPort == 0 {
		cfg.PublicPort = DefaultPublicPort
	}
	if cfg.PublicPort < 1 || cfg.PublicPort > 65535 {
		return nil, fmt.Errorf("certauth: invalid public port %d", cfg.PublicPort)
	}
	cert, err := serverCertificate(cfg.TLSCertPEM, cfg.TLSKeyPEM, host)
	if err != nil {
		return nil, err
	}
	v.serverCert = cert
	if v.origins, err = parseOrigins(cfg.AllowedOrigins); err != nil {
		return nil, err
	}
	if err := checkDefaultReturn(cfg.DefaultReturn, v.origins); err != nil {
		return nil, err
	}
	if len(cfg.RootKey) == 0 {
		return nil, fmt.Errorf("certauth: the root key is required")
	}
	v.maxFails = cfg.MaxHourlyFailures // negative: ClaimCSPCounter disables the cap
	if v.maxFails == 0 {
		v.maxFails = csp.DefaultMaxHourlyAuthFailures
	}
	if err := cfg.validateFronts(v); err != nil {
		return nil, err
	}
	if v.providers, err = cfg.buildProviders(); err != nil {
		return nil, err
	}
	if cfg.TestProvider {
		v.warnings = append(v.warnings, "the synthetic test provider is enabled: never run this against production")
	}
	if cfg.Mode == ModeCloudflare {
		v.warnings = append(v.warnings, "cloudflare mode is not production ready: CA advertisement, HTTP/2 coalescing, "+
			"no-certificate forwarding and the mTLS hostname association are unverified (spec 3b)")
	}
	return v, nil
}

// errCloudflareNotAcknowledged refuses cloudflare mode without its explicit opt-in: the forwarded
// certificate headers are only trustworthy behind a per-zone or per-hostname AOP certificate.
var errCloudflareNotAcknowledged = errors.New("certauth: cloudflare mode needs certAuthCloudflareAcknowledged: " +
	"set it only after the blockers in the spec section 3b are verified and the AOP CA is a per-zone or " +
	"per-hostname certificate, never Cloudflare's global one")

// validateFronts checks the PROXY protocol and cloudflare settings.
func (cfg *Config) validateFronts(v *validated) error {
	var err error
	if cfg.ProxyProtocol {
		if cfg.Mode != ModeOrigin {
			return fmt.Errorf("certauth: the PROXY protocol is only for origin mode")
		}
		if len(cfg.ProxyFrontCIDRs) == 0 {
			return fmt.Errorf("certauth: the PROXY protocol needs the front CIDRs allowed to send it")
		}
		if v.proxyNets, err = parseCIDRs(cfg.ProxyFrontCIDRs); err != nil {
			return err
		}
	}
	if cfg.Mode != ModeCloudflare {
		return nil
	}
	if !cfg.CFAcknowledged {
		return errCloudflareNotAcknowledged
	}
	v.cfCAs = x509.NewCertPool()
	if !v.cfCAs.AppendCertsFromPEM(cfg.CFOriginPullCAPEM) {
		return fmt.Errorf("certauth: cloudflare mode needs the Authenticated Origin Pulls CA (PEM)")
	}
	if v.cfNets, err = parseCIDRs(cfg.CFAllowedCIDRs); err != nil {
		return err
	}
	return nil
}

// buildProviders constructs the configured cert-check providers, plus the synthetic one when it is
// enabled against an allowlisted API.
func (cfg *Config) buildProviders() ([]certcheck.Provider, error) {
	var providers []certcheck.Provider
	for _, id := range cfg.Providers {
		newProvider, ok := providerConstructors[id]
		if !ok {
			return nil, fmt.Errorf("certauth: unknown certificate provider %q (fnmt, idcat)", id)
		}
		providers = append(providers, newProvider())
	}
	if cfg.TestProvider {
		if err := checkTestProviderAPI(cfg.VocdoniAPI, cfg.TestProviderAllowedAPIs); err != nil {
			return nil, err
		}
		p, err := testprovider.New(cfg.TestProviderCAPEM)
		if err != nil {
			return nil, fmt.Errorf("certauth: test provider: %w", err)
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("certauth: no certificate provider configured")
	}
	return providers, nil
}

// serverCertificate loads the auth listener certificate and checks it covers every host the
// listener answers on: a sample nonce host and the probe host.
func serverCertificate(certPEM, keyPEM []byte, host string) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("certauth: loading the TLS certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("certauth: parsing the TLS certificate: %w", err)
	}
	for _, name := range []string{strings.Repeat("a", nonceLen) + "." + host, probeLabel + "." + host} {
		if err := leaf.VerifyHostname(name); err != nil {
			return nil, fmt.Errorf("certauth: the TLS certificate does not cover %s: %w", name, err)
		}
	}
	pair.Leaf = leaf
	return &pair, nil
}

// parseOrigins validates the allowed origins: exact scheme://host[:port], https, or http for a
// localhost or 127.0.0.1 host.
func parseOrigins(list []string) (map[string]bool, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("certauth: at least one allowed origin is required")
	}
	origins := make(map[string]bool, len(list))
	for _, o := range list {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
			o != strings.ToLower(u.Scheme+"://"+u.Host) {
			return nil, fmt.Errorf("certauth: allowed origin %q must be a lower case scheme://host[:port]", o)
		}
		if !secureScheme(u) {
			return nil, fmt.Errorf("certauth: allowed origin %q must be https (http only for localhost)", o)
		}
		origins[o] = true
	}
	return origins, nil
}

// secureScheme reports whether u is https, or http on localhost or 127.0.0.1.
func secureScheme(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	default:
		return false
	}
}

// checkDefaultReturn requires the attempt_expired fallback to be an absolute URL on an allowed
// origin, so even that redirect never leaves the app.
func checkDefaultReturn(raw string, origins map[string]bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !origins[u.Scheme+"://"+u.Host] {
		return fmt.Errorf("certauth: the default return %q must be a URL on an allowed origin", raw)
	}
	return nil
}

// parseCIDRs parses a list of CIDRs.
func parseCIDRs(list []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(list))
	for _, s := range list {
		_, n, err := net.ParseCIDR(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("certauth: invalid CIDR %q: %w", s, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// checkTestProviderAPI refuses the synthetic provider unless the API host is allowlisted.
func checkTestProviderAPI(apiURL string, extra []string) error {
	u, err := url.Parse(apiURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("certauth: the test provider needs a valid --vocdoniApi URL, got %q", apiURL)
	}
	host := strings.ToLower(u.Hostname())
	if slices.Contains(testProviderAPIHosts, host) || slices.Contains(extra, host) {
		return nil
	}
	return fmt.Errorf("certauth: the test provider is refused against the API host %q", host)
}

// voterRefKey derives the voterRef HMAC key from the root key: stable across replicas and restarts.
func voterRefKey(rootKey []byte) []byte {
	sum := sha256.Sum256(append([]byte("vocdoni-certauth-voterref"), rootKey...))
	return sum[:]
}
