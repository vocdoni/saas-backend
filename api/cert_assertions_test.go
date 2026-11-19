package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// testCertPKCE returns a fresh PKCE verifier and its S256 challenge.
func testCertPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	random := make([]byte, 32)
	_, err := rand.Read(random)
	qt.Assert(t, err, qt.IsNil)
	verifier = base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// newCertAssertionRequest is the assertion the gateway posts for a certificate of nationalID. The
// certificate hash is random, so two requests never share a body (a replayed signature is refused).
func newCertAssertionRequest(pid, nationalID, challenge string) *handlers.CertAssertionRequest {
	certSHA256 := make([]byte, sha256.Size)
	_, _ = rand.Read(certSHA256)
	return &handlers.CertAssertionRequest{
		ProcessID: pid, NationalID: nationalID, DocumentType: "DNI", Provider: "fnmt",
		Profile: "fnmt-citizen-g2", CertSHA256: hex.EncodeToString(certSHA256), CodeChallenge: challenge,
	}
}

// postCertAssertionRaw posts body to the assertion route with signatureHeader, returning the
// answer's status and body.
func postCertAssertionRaw(t *testing.T, body []byte, signatureHeader string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, testURL(certAuthAssertionsEndpoint), bytes.NewReader(body))
	qt.Assert(t, err, qt.IsNil)
	req.Header.Set("Content-Type", "application/json")
	if signatureHeader != "" {
		req.Header.Set(csp.CertGatewaySignatureHeader, signatureHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	qt.Assert(t, err, qt.IsNil)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	qt.Assert(t, err, qt.IsNil)
	return resp.StatusCode, data
}

// signCertAssertion returns the X-Cert-Gateway-Signature of body at time at with key keyID.
func signCertAssertion(key ed25519.PrivateKey, keyID string, body []byte, at time.Time) string {
	sig := ed25519.Sign(key, csp.CertGatewaySignedMessage(at.Unix(), body))
	return "t=" + strconv.FormatInt(at.Unix(), 10) + ",k=" + keyID + ",v1=" + base64.RawURLEncoding.EncodeToString(sig)
}

// postCertAssertion signs req with the test vector key and posts it.
func postCertAssertion(t *testing.T, req *handlers.CertAssertionRequest) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(req)
	qt.Assert(t, err, qt.IsNil)
	return postCertAssertionRaw(t, body, signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now()))
}

// newCertCode gets a certificate code for nationalID on process pid, bound to challenge.
func newCertCode(t *testing.T, pid, nationalID, challenge string) string {
	t.Helper()
	status, body := postCertAssertion(t, newCertAssertionRequest(pid, nationalID, challenge))
	qt.Assert(t, status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", body))
	var resp handlers.CertAssertionResponse
	qt.Assert(t, json.Unmarshal(body, &resp), qt.IsNil)
	return resp.CertCode
}

// TestCertAssertions (AC4) covers POST /cert-auth/assertions: the signature, and each row of the
// response contract with its exact status and body.
func TestCertAssertions(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "A-1", Name: "Alice", NationalID: "1234567-l", Weight: "2"},
		apicommon.OrgMember{MemberNumber: "A-2", Name: "Bob", NationalID: testDNI(2), Weight: "0"},
		apicommon.OrgMember{MemberNumber: "A-3", Name: "Carol", NationalID: testDNI(3), Weight: "1"},
	)
	ids := memberIDs(members)
	spec := certCensus(nil, ids...)
	spec.Weighted = true
	pid, _ := publishCensusProcess(t, token, orgAddress, spec, 1)
	_, challenge := testCertPKCE(t)

	expect := func(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
		t.Helper()
		qt.Assert(t, status, qt.Equals, wantStatus, qt.Commentf("response: %s", body))
		qt.Assert(t, string(body), qt.Equals, `{"error":"`+wantCode+`"}`+"\n")
	}

	t.Run("a valid signature creates a code, stored without the DNI", func(t *testing.T) {
		c := qt.New(t)
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, "01234567L", challenge))
		c.Assert(status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", body))
		var resp handlers.CertAssertionResponse
		c.Assert(json.Unmarshal(body, &resp), qt.IsNil)
		c.Assert(resp.CertCode, qt.Matches, `[A-Za-z0-9_-]{43}`)
		expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		c.Assert(err, qt.IsNil)
		c.Assert(resp.ExpiresAt, qt.Matches, `.*Z`)
		c.Assert(time.Until(expiresAt) > 4*time.Minute && time.Until(expiresAt) <= 5*time.Minute, qt.IsTrue)

		codeHash := sha256.Sum256([]byte(resp.CertCode))
		var stored bson.M
		c.Assert(testDB.DBClient.Database(testDBName).Collection("certAuthAssertions").
			FindOne(context.Background(), bson.M{"codeHash": codeHash[:]}).Decode(&stored), qt.IsNil)
		c.Assert(stored["participantId"], qt.Equals, ids[0])
		raw, err := json.Marshal(stored)
		c.Assert(err, qt.IsNil)
		c.Assert(string(raw), qt.Not(qt.Contains), "1234567")
	})

	t.Run("bad signatures", func(t *testing.T) {
		body, err := json.Marshal(newCertAssertionRequest(pid, testDNI(3), challenge))
		qt.Assert(t, err, qt.IsNil)
		otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
		for name, header := range map[string]string{
			"missing":     "",
			"malformed":   "v1=abc",
			"stale":       signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now().Add(-2*time.Minute)),
			"unknown key": signCertAssertion(testCertGatewayKey, "test-2", body, time.Now()),
			"wrong key":   signCertAssertion(otherKey, testCertGatewayKeyID, body, time.Now()),
		} {
			t.Run(name, func(t *testing.T) {
				status, resp := postCertAssertionRaw(t, body, header)
				expect(t, status, resp, http.StatusUnauthorized, "bad_signature")
			})
		}
	})

	t.Run("a replayed request is refused, a different one accepted", func(t *testing.T) {
		body, err := json.Marshal(newCertAssertionRequest(pid, "01234567L", challenge))
		qt.Assert(t, err, qt.IsNil)
		header := signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now())
		status, resp := postCertAssertionRaw(t, body, header)
		qt.Assert(t, status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", resp))
		status, resp = postCertAssertionRaw(t, body, header)
		expect(t, status, resp, http.StatusUnauthorized, "bad_signature")
		status, resp = postCertAssertion(t, newCertAssertionRequest(pid, "01234567L", challenge))
		qt.Assert(t, status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", resp))
	})

	t.Run("malformed requests", func(t *testing.T) {
		for _, req := range []*handlers.CertAssertionRequest{
			newCertAssertionRequest("not-an-id", testDNI(3), challenge),
			newCertAssertionRequest(bson.NewObjectID().Hex()[:23]+"G", testDNI(3), challenge),
			newCertAssertionRequest(pid, "12345678A", challenge), // invalid DNI
			newCertAssertionRequest(pid, testDNI(3), "short"),
		} {
			status, body := postCertAssertion(t, req)
			expect(t, status, body, http.StatusBadRequest, "bad_request")
		}
		body := []byte(`{"processId":`)
		status, resp := postCertAssertionRaw(t, body, signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now()))
		expect(t, status, resp, http.StatusBadRequest, "bad_request")
	})

	t.Run("process unknown or not published", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(bson.NewObjectID().Hex(), testDNI(3), challenge))
		expect(t, status, body, http.StatusNotFound, "process_not_open")
		draft := minimalVotingProcessRequest(orgAddress)
		draft.Census = certCensus(nil, ids...)
		draftID := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, draft,
			processesCreateEndpoint).ProcessID
		status, body = postCertAssertion(t, newCertAssertionRequest(draftID, testDNI(3), challenge))
		expect(t, status, body, http.StatusNotFound, "process_not_open")
	})

	t.Run("process ended", func(t *testing.T) {
		c := qt.New(t)
		endedPID, _ := publishCensusProcess(t, token, orgAddress, certCensus(nil, ids...), 1)
		vp, err := testDB.VotingProcess(objectID(c, endedPID))
		c.Assert(err, qt.IsNil)
		vp.EndDate = time.Now().Add(-2 * time.Minute) // past the CSP's end grace; publish refuses a past one
		_, err = testDB.SetVotingProcess(vp)
		c.Assert(err, qt.IsNil)
		status, body := postCertAssertion(t, newCertAssertionRequest(endedPID, testDNI(3), challenge))
		expect(t, status, body, http.StatusNotFound, "process_not_open")
	})

	t.Run("census without certAuth", func(t *testing.T) {
		typed, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}, MemberIDs: ids,
		}, 1)
		status, body := postCertAssertion(t, newCertAssertionRequest(typed, testDNI(3), challenge))
		expect(t, status, body, http.StatusConflict, "not_cert_census")
	})

	t.Run("anonymous census while anonymous voting is off", func(t *testing.T) {
		anonymous := certCensus(nil, ids...)
		anonymous.Anonymous = true
		anonPID, _ := publishCensusProcess(t, token, orgAddress, anonymous, 1)
		setDisableBlindCSP(t, true)
		status, body := postCertAssertion(t, newCertAssertionRequest(anonPID, testDNI(3), challenge))
		expect(t, status, body, http.StatusConflict, "anonymous_unavailable")
	})

	t.Run("not in census", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(9), challenge))
		expect(t, status, body, http.StatusNotFound, "not_in_census")
	})

	t.Run("zero weight", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(2), challenge))
		expect(t, status, body, http.StatusForbidden, "zero_weight")
	})

	t.Run("rate limited after 10 an hour", func(t *testing.T) {
		for range csp.MaxHourlyCertAssertions {
			newCertCode(t, pid, testDNI(3), challenge)
		}
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(3), challenge))
		expect(t, status, body, http.StatusTooManyRequests, "rate_limited")
	})

	t.Run("feature off: a plain 404", func(t *testing.T) {
		setCertGatewayKeys(t, nil)
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(3), challenge))
		qt.Assert(t, status, qt.Equals, http.StatusNotFound)
		qt.Assert(t, body, qt.HasLen, 0)
	})
	c.Assert(testAPI.csp.CertAuthEnabled(), qt.IsTrue)
}
