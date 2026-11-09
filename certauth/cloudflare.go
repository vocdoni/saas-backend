package certauth

import (
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
)

// Headers Cloudflare forwards the browser's client certificate in (spec section 3b).
const (
	// headerClientCert is the RFC 9440 leaf, a structured-field byte sequence (":<base64>:").
	headerClientCert = "Client-Cert"
	// headerClientCertChain is the RFC 9440 chain without the leaf, a list of byte sequences.
	headerClientCertChain = "Client-Cert-Chain"
	// headerCFClientCertDER is Cloudflare's legacy leaf header, plain base64 DER.
	headerCFClientCertDER = "Cf-Client-Cert-Der-Base64"
)

// cloudflareTLSConfig is the cloudflare-mode TLS configuration: only a peer presenting the zone's
// Authenticated Origin Pulls certificate completes a handshake. With the optional peer allowlist,
// that is what makes the forwarded certificate headers trustworthy.
func (s *Service) cloudflareTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{alpnHTTP1},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.v.serverCert, nil },
		ClientCAs:      s.v.cfCAs,
		ClientAuth:     tls.RequireAndVerifyClientCert,
	}
}

// cloudflareChain rebuilds the presented chain from the forwarded headers: the RFC 9440 leaf, or
// the legacy one, then the RFC 9440 chain. No leaf header means no certificate; a leaf header that
// does not decode is a certificate that was presented but cannot be kept (redeem: invalid_chain).
func cloudflareChain(h http.Header) ([][]byte, bool) {
	leafHeader, legacy := strings.TrimSpace(h.Get(headerClientCert)), false
	if leafHeader == "" {
		leafHeader, legacy = strings.TrimSpace(h.Get(headerCFClientCertDER)), true
	}
	if leafHeader == "" {
		return nil, false
	}
	var leaf []byte
	if legacy {
		leaf, _ = base64.StdEncoding.DecodeString(leafHeader)
	} else {
		leaf = sfByteSequence(leafHeader)
	}
	if len(leaf) == 0 {
		return nil, true
	}
	chain := [][]byte{leaf}
	for _, value := range h.Values(headerClientCertChain) {
		for item := range strings.SplitSeq(value, ",") {
			if der := sfByteSequence(strings.TrimSpace(item)); len(der) > 0 {
				chain = append(chain, der)
			}
		}
	}
	return chain, true
}

// sfByteSequence decodes an RFC 8941 byte sequence (":<base64>:"), or returns nil.
func sfByteSequence(item string) []byte {
	inner, ok := strings.CutPrefix(item, ":")
	if !ok {
		return nil
	}
	inner, ok = strings.CutSuffix(inner, ":")
	if !ok {
		return nil
	}
	der, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return nil
	}
	return der
}

// peerFilter drops, at accept time, every connection from a peer outside the allowed networks.
type peerFilter struct {
	net.Listener
	allowed []*net.IPNet
}

// Accept implements net.Listener.
func (f *peerFilter) Accept() (net.Conn, error) {
	for {
		conn, err := f.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if ipIn(conn.RemoteAddr(), f.allowed) {
			return conn, nil
		}
		_ = conn.Close()
	}
}
