package csp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// Shared certificate gateway signature test vectors. cert-gateway's internal/signing tests use the
// same values: the seed is bytes 0x00..0x1f, its public key is vectorPublicKey.
const (
	vectorSeed      = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	vectorPublicKey = "A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg="
	vectorKeyID     = "test-1"
	vectorTime      = int64(1767225600) // 2026-01-01T00:00:00Z
	vectorBody      = `{"processId":"0123456789abcdef01234567","nationalId":"12345678Z","documentType":"DNI",` +
		`"provider":"fnmt","profile":"fnmt-citizen-g2",` +
		`"certSha256":"abababababababababababababababababababababababababababababababab",` +
		`"codeChallenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}`
	vectorMessage = "cert-gateway-v1\n1767225600\nPOST\n/cert-auth/assertions\n" +
		"4c6ce4c5f31980d9aa0819615924fc66ab54794461d5a4602fda03144362a1ff"
	vectorHeader = "t=1767225600,k=test-1,v1=" +
		"MIU3KNz-CoWDXqKlIFuxleuc_y5cG83YtE6v84OAb9mfZRp4jDnnPXOSTaVn666rdPe7SERtWnL2uGi6qH9QAg"
)

func TestCertGatewaySignatureVectors(t *testing.T) {
	c := qt.New(t)
	// a second, rotated-out key next to the vector key
	keys, err := ParseCertGatewayKeys("old-1:11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=, " + vectorKeyID + ":" + vectorPublicKey)
	c.Assert(err, qt.IsNil)
	c.Assert(keys, qt.HasLen, 2)
	at := time.Unix(vectorTime, 0)
	verify := func(header, body string, receivedAt time.Time) ([]byte, error) {
		return VerifyCertGatewaySignature(keys, CertGatewaySignedRequest{Header: header, Body: []byte(body), ReceivedAt: receivedAt})
	}

	c.Assert(string(CertGatewaySignedMessage(vectorTime, []byte(vectorBody))), qt.Equals, vectorMessage)
	digest, err := verify(vectorHeader, vectorBody, at)
	c.Assert(err, qt.IsNil)
	wantDigest := sha256.Sum256([]byte(vectorMessage))
	c.Assert(digest, qt.DeepEquals, wantDigest[:])
	_, err = verify(vectorHeader, vectorBody, at.Add(60*time.Second))
	c.Assert(err, qt.IsNil)
	_, err = verify(vectorHeader, vectorBody, at.Add(-60*time.Second))
	c.Assert(err, qt.IsNil)
	// the digest names the signed request, not how its header is written
	parts := strings.Split(vectorHeader, ",")
	reordered := strings.Join([]string{parts[2], parts[0], parts[1]}, ",")
	digest2, err := verify(reordered, vectorBody, at)
	c.Assert(err, qt.IsNil)
	c.Assert(digest2, qt.DeepEquals, digest)

	for name, tc := range map[string]struct {
		header, body string
		at           time.Time
	}{
		"empty header":    {"", vectorBody, at},
		"missing v1":      {"t=1767225600,k=test-1", vectorBody, at},
		"duplicate field": {vectorHeader + ",k=test-1", vectorBody, at},
		"unknown key":     {"t=1767225600,k=test-2,v1=" + vectorHeader[len("t=1767225600,k=test-1,v1="):], vectorBody, at},
		"stale":           {vectorHeader, vectorBody, at.Add(61 * time.Second)},
		"future":          {vectorHeader, vectorBody, at.Add(-61 * time.Second)},
		"padded":          {vectorHeader + "==", vectorBody, at},
		"other body":      {vectorHeader, vectorBody + " ", at},
		"other time":      {"t=1767225601" + vectorHeader[len("t=1767225600"):], vectorBody, at},
	} {
		_, err := verify(tc.header, tc.body, tc.at)
		c.Assert(errors.Is(err, ErrCertGatewaySignature), qt.IsTrue, qt.Commentf("%s: %v", name, err))
	}
}

func TestParseCertGatewayKeys(t *testing.T) {
	c := qt.New(t)
	keys, err := ParseCertGatewayKeys("  ")
	c.Assert(err, qt.IsNil)
	c.Assert(keys, qt.HasLen, 0)
	for _, bad := range []string{
		"test-1",              // no key
		":" + vectorPublicKey, // no id
		"test-1:not-base64!",  // not base64
		"test-1:AAEC",         // wrong length
		"test-1:" + vectorPublicKey + ",test-1:" + vectorPublicKey, // duplicate id
		"test=1:" + vectorPublicKey,                                // '=' breaks the signature header
	} {
		_, err := ParseCertGatewayKeys(bad)
		c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("%q", bad))
	}
}

func TestVoterRef(t *testing.T) {
	c := qt.New(t)
	key := voterRefKey("secret")
	keyHash := sha256.Sum256([]byte("vocdoni-voter-ref-key:secret"))
	c.Assert(key, qt.DeepEquals, keyHash[:])
	mac := hmac.New(sha256.New, keyHash[:])
	mac.Write([]byte("vocdoni-voter-ref:process:member-1"))
	ref := voterRef(key, "process", "member-1")
	c.Assert(ref, qt.Equals, hex.EncodeToString(mac.Sum(nil)))
	c.Assert(ref, qt.Matches, `[0-9a-f]{64}`)
	c.Assert(voterRef(key, "process", "member-1"), qt.Equals, ref) // stable across logins
	c.Assert(voterRef(key, "process", "member-2"), qt.Not(qt.Equals), ref)
	c.Assert(voterRef(voterRefKey("other secret"), "process", "member-1"), qt.Not(qt.Equals), ref)
}
