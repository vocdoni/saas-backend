package certauth

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"regexp"
	"strings"
)

// nonceEncoding writes the 16 random bytes of a nonce as a 26-character DNS label.
var nonceEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

var (
	// tokenRe is the grammar of a state, a code and an S256 challenge: 32 bytes, base64url, no padding.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`) //nolint:unused
	// nonceRe is the grammar of a nonce host label.
	nonceRe = regexp.MustCompile(`^[a-z2-7]{26}$`) //nolint:unused
	// verifierRe is the RFC 7636 code verifier grammar.
	verifierRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`) //nolint:unused
)

// randomToken returns 32 random bytes as base64url without padding (43 characters): a state or a code.
func randomToken() string { //nolint:unused
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// randomNonce returns 16 random bytes as a lower case base32 label (26 characters), independent of
// any state: an observer of the DNS or the SNI cannot link the host to the redeem.
func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToLower(nonceEncoding.EncodeToString(b))
}
