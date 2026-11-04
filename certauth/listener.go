package certauth

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

const (
	// maxStoredChainCerts and maxStoredChainBytes bound the chain an attempt stores (spec 3, step 2).
	maxStoredChainCerts = certcheck.DefaultMaxChainLen
	maxStoredChainBytes = 200 << 10
	// listener socket deadlines: no request body is ever read.
	listenerReadHeaderTimeout = 10 * time.Second
	listenerReadTimeout       = 30 * time.Second
	listenerWriteTimeout      = 30 * time.Second
	// proxyHeaderTimeout bounds the wait for a PROXY protocol header.
	proxyHeaderTimeout = 10 * time.Second
)

// hostKind is what a host name of the auth listener addresses.
type hostKind int

const (
	hostUnknown hostKind = iota
	hostProbe
	hostAttempt
)

// errUnknownServerName refuses a handshake for a name the listener does not serve.
var errUnknownServerName = errors.New("certauth: unknown server name")

// classifyHost tells what a lower case host name (no port) addresses on the auth listener, and
// returns the nonce of an attempt host.
func (s *Service) classifyHost(host string) (hostKind, string) {
	label, rest, found := strings.Cut(host, ".")
	if !found || rest != s.cfg.Host {
		return hostUnknown, ""
	}
	switch {
	case label == probeLabel:
		return hostProbe, ""
	case nonceRe.MatchString(label):
		return hostAttempt, label
	default:
		return hostUnknown, ""
	}
}

// ListenAndServe serves the auth listener on the configured address until it fails.
func (s *Service) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("certauth: listening on %s: %w", s.cfg.ListenAddr, err)
	}
	return s.Serve(ln)
}

// Serve serves the auth listener on a TCP listener: the PROXY protocol (origin mode) or the peer
// filter (cloudflare mode) first, then TLS, then HTTP/1.1 only.
func (s *Service) Serve(ln net.Listener) error {
	tlsLn := tls.NewListener(s.wrapListener(ln), s.listenerTLSConfig())
	srv := &http.Server{
		Handler:           s.listenerHandler(),
		ReadHeaderTimeout: listenerReadHeaderTimeout,
		ReadTimeout:       listenerReadTimeout,
		WriteTimeout:      listenerWriteTimeout,
		// handshake failures (scanners, cancelled pickers) are routine on a public port
		ErrorLog: stdlog.New(debugWriter{}, "", 0),
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	return srv.Serve(tlsLn)
}

// debugWriter sends the http.Server's own error lines to the debug log.
type debugWriter struct{}

func (debugWriter) Write(p []byte) (int, error) {
	log.Debugw("certificate auth listener", "event", string(bytes.TrimSpace(p)))
	return len(p), nil
}

// wrapListener applies the front of the mode: in origin mode the PROXY protocol, honoured only from
// the front CIDRs (any other peer is plain TCP); in cloudflare mode the peer allowlist.
func (s *Service) wrapListener(ln net.Listener) net.Listener {
	switch {
	case s.cfg.Mode == ModeOrigin && s.cfg.ProxyProtocol:
		return &proxyproto.Listener{Listener: ln, ConnPolicy: s.proxyPolicy, ReadHeaderTimeout: proxyHeaderTimeout}
	case s.cfg.Mode == ModeCloudflare && len(s.v.cfNets) > 0:
		return &peerFilter{Listener: ln, allowed: s.v.cfNets}
	default:
		return ln
	}
}

// proxyPolicy reads a PROXY header only from a front CIDR: from anyone else the connection is plain
// TCP, so nobody else can choose the client IP the logs record.
func (s *Service) proxyPolicy(opts proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
	if ipIn(opts.Upstream, s.v.proxyNets) {
		return proxyproto.USE, nil
	}
	return proxyproto.SKIP, nil
}

// ipIn reports whether a TCP address is inside one of the networks.
func ipIn(addr net.Addr, nets []*net.IPNet) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	for _, n := range nets {
		if n.Contains(tcp.IP) {
			return true
		}
	}
	return false
}

// listenerTLSConfig is the TLS configuration of the mode.
func (s *Service) listenerTLSConfig() *tls.Config {
	if s.cfg.Mode == ModeCloudflare {
		return s.cloudflareTLSConfig()
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{alpnHTTP1},
		GetCertificate:     func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.v.serverCert, nil },
		GetConfigForClient: s.configForClient,
	}
}

// configForClient selects the origin-mode configuration by SNI: the probe host never requests a
// client certificate (a background probe must not open the picker), an attempt host gets
// cert-check's configuration, and any other name fails the handshake.
func (s *Service) configForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	switch kind, _ := s.classifyHost(strings.ToLower(hello.ServerName)); kind {
	case hostProbe:
		return s.probeTLS, nil
	case hostAttempt:
		return s.authTLS, nil
	default:
		return nil, errUnknownServerName
	}
}

// listenerHandler routes by Host: the probe, an attempt, or 421 for anything else.
func (s *Service) listenerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch kind, nonce := s.classifyHost(host); kind {
		case hostProbe:
			s.serveProbe(w, r)
		case hostAttempt:
			s.serveAuthenticate(w, r, nonce)
		default:
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		}
	})
}

// serveProbe answers GET /ok on the probe host (contract section 7).
func (s *Service) serveProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != probePath {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); s.v.origins[origin] {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	h.Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// serveAuthenticate is GET /v1/authenticate?state= on a nonce host (contract section 4): it stores
// the presented chain (or none) in the attempt and sends the browser straight back with a code. It
// never verifies anything and never answers anything but a 303 to an allowlisted URL.
func (s *Service) serveAuthenticate(w http.ResponseWriter, r *http.Request, nonce string) {
	if r.Method != http.MethodGet || r.URL.Path != authenticatePath {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Mode == ModeOrigin && certcheck.CheckHost(r) != nil {
		http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		return
	}
	state := r.URL.Query().Get("state")
	raw, presented := s.presentedChain(r)
	chain := boundChain(raw)
	if !tokenRe.MatchString(state) {
		s.redirect(w, s.cfg.DefaultReturn, url.Values{"error": {"attempt_expired"}})
		return
	}
	now := s.now()
	code := randomToken()
	tx, err := s.store.ConsumeCertAuthState(db.CertAuthStateConsume{
		State: state, Nonce: nonce, Code: code, Chain: chain, ChainPresented: presented,
		CreatedAfter: now.Add(-stateTTL), Now: now, ExpiresAt: now.Add(codeTTL),
	})
	if err != nil {
		s.redirect(w, s.expiredReturn(state, nonce, err), url.Values{"error": {"attempt_expired"}})
		return
	}
	s.log.Infow("certificate attempt authenticated", "transaction", tx.ID.Hex(), "processId", tx.ProcessID.Hex(),
		"certificatePresented", presented, "chainLength", len(chain), "clientIP", s.listenerClientIP(r))
	s.redirect(w, tx.ReturnURL, url.Values{"code": {code}, "state": {state}})
}

// expiredReturn is where an unusable state is sent back to: its own attempt's return URL when the
// attempt is still readable on this host, the default return otherwise.
func (s *Service) expiredReturn(state, nonce string, consumeErr error) string {
	if !errors.Is(consumeErr, db.ErrNotFound) {
		s.log.Warnw("could not consume a certificate attempt", "error", consumeErr)
		return s.cfg.DefaultReturn
	}
	tx, err := s.store.CertAuthTransactionByState(state, nonce)
	if err != nil {
		return s.cfg.DefaultReturn
	}
	return tx.ReturnURL
}

// redirect answers a bodiless 303 to target with the fragment, never cached, never leaking a
// referrer, and closing the connection so the next attempt opens a fresh handshake.
func (*Service) redirect(w http.ResponseWriter, target string, fragment url.Values) {
	h := w.Header()
	// the fragment values are base64url or fixed reason strings: Encode leaves them as they are
	h.Set("Location", target+"#"+fragment.Encode())
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Connection", "close")
	w.WriteHeader(http.StatusSeeOther)
}

// presentedChain returns the DER chain the browser presented, leaf first, and whether it presented
// one at all: the TLS peer certificates in origin mode, the forwarded headers in cloudflare mode.
func (s *Service) presentedChain(r *http.Request) ([][]byte, bool) {
	if s.cfg.Mode == ModeCloudflare {
		return cloudflareChain(r.Header)
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, false
	}
	chain := make([][]byte, 0, len(r.TLS.PeerCertificates))
	for _, c := range r.TLS.PeerCertificates {
		chain = append(chain, c.Raw)
	}
	return chain, true
}

// boundChain bounds what an attempt stores: a chain over maxStoredChainCerts certificates or
// maxStoredChainBytes keeps its leaf alone when the leaf is within cert-check's size bound (Verify
// judges the leaf against its pinned CAs), and nothing when it is not (redeem answers invalid_chain).
func boundChain(chain [][]byte) [][]byte {
	if len(chain) == 0 {
		return nil
	}
	total := 0
	for _, c := range chain {
		total += len(c)
	}
	if len(chain) <= maxStoredChainCerts && total <= maxStoredChainBytes {
		return chain
	}
	if len(chain[0]) <= certcheck.MaxCertificateDER {
		return chain[:1]
	}
	return nil
}

// listenerClientIP is the client IP the logs record: the TCP peer (the PROXY header's source when a
// front sent one), or Cloudflare's CF-Connecting-IP in cloudflare mode, where the peer is Cloudflare.
func (s *Service) listenerClientIP(r *http.Request) string {
	if s.cfg.Mode == ModeCloudflare {
		if ip := net.ParseIP(r.Header.Get("CF-Connecting-IP")); ip != nil {
			return ip.String()
		}
	}
	return clientIP(r.RemoteAddr)
}

// publicHost returns host with the public port appended when it is not 443.
func (s *Service) publicHost(host string) string {
	if s.cfg.PublicPort == DefaultPublicPort {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(s.cfg.PublicPort))
}

// clientIP returns the IP of the peer of a TCP address, or the address itself.
func clientIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
