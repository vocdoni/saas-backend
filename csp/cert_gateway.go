package csp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// CertGatewaySignatureHeader carries the certificate gateway's signature of an assertion request.
const CertGatewaySignatureHeader = "X-Cert-Gateway-Signature"

// CertGatewaySignatureMaxSkew is how far the signature timestamp may be from the server clock.
const CertGatewaySignatureMaxSkew = 60 * time.Second

// certGatewaySignedPath is the request path the signature covers. It is a literal on both sides,
// never read from the request, so a path prefix or rewrite at the edge cannot break verification.
const certGatewaySignedPath = "/cert-auth/assertions"

// ErrCertGatewaySignature is returned for a missing, malformed, stale, unknown-key or invalid
// certificate gateway signature.
var ErrCertGatewaySignature = fmt.Errorf("invalid certificate gateway signature")

// CertGatewayKeys maps a certificate gateway key id to its Ed25519 public key. Several keys can be
// configured at once, so a key can be rotated without downtime.
type CertGatewayKeys map[string]ed25519.PublicKey

// ParseCertGatewayKeys parses CERT_GATEWAY_PUBLIC_KEYS: a comma-separated list of
// keyId:base64(public key) entries. An empty value is no keys (the feature is off).
func ParseCertGatewayKeys(s string) (CertGatewayKeys, error) {
	keys := CertGatewayKeys{}
	if strings.TrimSpace(s) == "" {
		return keys, nil
	}
	for entry := range strings.SplitSeq(s, ",") {
		id, encoded, found := strings.Cut(strings.TrimSpace(entry), ":")
		if !found || id == "" || strings.ContainsAny(id, ",= ") {
			return nil, fmt.Errorf("certificate gateway key %q: want keyId:base64(publicKey)", id)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("certificate gateway key %q: duplicate key id", id)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("certificate gateway key %q: not a base64 Ed25519 public key", id)
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	return keys, nil
}

// CertGatewaySignedMessage returns the bytes the certificate gateway signs for an assertion
// request body sent at unix time t.
func CertGatewaySignedMessage(t int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("cert-gateway-v1\n" + strconv.FormatInt(t, 10) + "\nPOST\n" + certGatewaySignedPath + "\n" +
		hex.EncodeToString(sum[:]))
}

// VerifyCertGatewaySignature verifies header, the X-Cert-Gateway-Signature value
// "t=<unix seconds>,k=<keyId>,v1=<base64url(signature), no padding>", over body, the raw request
// body, with the key named by k. The timestamp must be within CertGatewaySignatureMaxSkew of now.
// Every failure wraps ErrCertGatewaySignature. On success it returns the request's digest, the
// sha256 of the signed message: the same for every copy of one signed request, however its header
// is written, so the caller can accept it once (CertGatewayReplayWindow).
func VerifyCertGatewaySignature(keys CertGatewayKeys, header string, body []byte, now time.Time) ([]byte, error) {
	fields := map[string]string{}
	for part := range strings.SplitSeq(header, ",") {
		name, value, found := strings.Cut(part, "=")
		if _, dup := fields[name]; !found || dup {
			return nil, fmt.Errorf("%w: malformed header", ErrCertGatewaySignature)
		}
		fields[name] = value
	}
	if len(fields) != 3 {
		return nil, fmt.Errorf("%w: malformed header", ErrCertGatewaySignature)
	}
	t, err := strconv.ParseInt(fields["t"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed timestamp", ErrCertGatewaySignature)
	}
	if skew := now.Sub(time.Unix(t, 0)); skew > CertGatewaySignatureMaxSkew || skew < -CertGatewaySignatureMaxSkew {
		return nil, fmt.Errorf("%w: stale timestamp", ErrCertGatewaySignature)
	}
	key, ok := keys[fields["k"]]
	if !ok {
		return nil, fmt.Errorf("%w: unknown key", ErrCertGatewaySignature)
	}
	sig, err := base64.RawURLEncoding.DecodeString(fields["v1"])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: malformed signature", ErrCertGatewaySignature)
	}
	message := CertGatewaySignedMessage(t, body)
	if !ed25519.Verify(key, message, sig) {
		return nil, fmt.Errorf("%w: signature mismatch", ErrCertGatewaySignature)
	}
	digest := sha256.Sum256(message)
	return digest[:], nil
}

// CertGatewayReplayWindow is how long an accepted request's digest is remembered: a signature is
// fresh for CertGatewaySignatureMaxSkew either side of its timestamp, so it is stale by then.
const CertGatewayReplayWindow = 2 * CertGatewaySignatureMaxSkew

// MaxHourlyCertAssertions caps the certificate assertions created for one census participant on
// one voting process per UTC hour.
const MaxHourlyCertAssertions = 10

// ClaimCertAssertion spends one unit of the participant's hourly certificate assertion budget on
// the process, and reports false once it is spent. The caller hands a granted claim to
// ReleaseCertAssertion when the assertion then fails to be created.
func (c *CSP) ClaimCertAssertion(processID, participantID internal.HexBytes) (db.CSPCounterClaim, bool, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterCertAssertions,
		counterKey(participantID, processID), MaxHourlyCertAssertions)
	if err != nil {
		return nil, false, fmt.Errorf("claiming cert assertion: %w", err)
	}
	return res.Claim, res.Granted, nil
}

// ReleaseCertAssertion gives back a ClaimCertAssertion unit whose assertion was not created.
// Storage errors are logged: at worst the participant loses one unit of the current hour.
func (c *CSP) ReleaseCertAssertion(claim db.CSPCounterClaim) {
	if err := c.Storage.ReleaseCSPCounter(claim); err != nil {
		log.Warnw("error releasing cert assertion", "error", err)
	}
}
