package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

// certStep0 redeems a certificate code at step 0 and returns the raw answer.
func certStep0(t *testing.T, pid string, req *handlers.AuthRequest) ([]byte, int) {
	t.Helper()
	return testRequest(t, http.MethodPost, "", req, processAuthURL(pid, "0"))
}

// captureAPILogs runs fn with the global logger writing to a file, and returns what it logged.
func captureAPILogs(t *testing.T, fn func()) string {
	t.Helper()
	prev := *log.Logger()
	t.Cleanup(func() { *log.Logger() = prev })
	logFile := filepath.Join(t.TempDir(), "api.log")
	f, err := os.Create(logFile)
	qt.Assert(t, err, qt.IsNil)
	t.Cleanup(func() { _ = f.Close() })
	stdout := os.Stdout
	os.Stdout = f
	log.Init("debug", "stdout", nil)
	os.Stdout = stdout
	fn()
	out, err := os.ReadFile(logFile)
	qt.Assert(t, err, qt.IsNil)
	return string(out)
}

// TestCertAuthStep0 (AC5, AC11) covers step 0 with a certificate code.
func TestCertAuthStep0(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{
			MemberNumber: "S-1", Name: "Alice", Surname: "Uno", NationalID: "1234567-l",
			Email: "alice-cert@example.com", Phone: "+34611111111",
		},
		apicommon.OrgMember{
			MemberNumber: "S-2", Name: "Bob", Surname: "Dos", NationalID: testDNI(2),
			Email: "bob-cert@example.com", Phone: "+34622222222",
		},
	)
	ids := memberIDs(members)
	alice, bob := "01234567L", testDNI(2)
	authOnly, authOnlyElections := publishCensusProcess(t, token, orgAddress, certCensus(nil, ids...), 1)
	withEmail, _ := publishCensusProcess(t, token, orgAddress,
		certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...), 1)
	typed, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}, MemberIDs: ids,
	}, 1)

	login := func(t *testing.T, pid, nationalID string, contact *handlers.AuthRequest) handlers.AuthResponse {
		t.Helper()
		verifier, challenge := testCertPKCE(t)
		req := &handlers.AuthRequest{CertCode: newCertCode(t, pid, nationalID, challenge), CodeVerifier: verifier}
		if contact != nil {
			req.Email, req.Phone = contact.Email, contact.Phone
		}
		body, code := certStep0(t, pid, req)
		qt.Assert(t, code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", body))
		var resp handlers.AuthResponse
		qt.Assert(t, json.Unmarshal(body, &resp), qt.IsNil)
		qt.Assert(t, resp.AuthToken, qt.Not(qt.HasLen), 0)
		return resp
	}

	t.Run("auth-only: a verified token that signs, and a stable voterRef", func(t *testing.T) {
		c := qt.New(t)
		first := login(t, authOnly, alice, nil)
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		testCSPSign(t, authOnly, first.AuthToken, authOnlyElections[0], voter.Address().Bytes())

		c.Assert(first.VoterRef, qt.Matches, `[0-9a-f]{64}`)
		sum := sha256.Sum256([]byte("vocdoni-voter-ref:" + authOnly + ":" + ids[0]))
		c.Assert(first.VoterRef, qt.Equals, hex.EncodeToString(sum[:]))
		c.Assert(login(t, authOnly, alice, nil).VoterRef, qt.Equals, first.VoterRef)
		c.Assert(login(t, authOnly, bob, nil).VoterRef, qt.Not(qt.Equals), first.VoterRef)
	})

	t.Run("email 2FA: an OTP, then step 1", func(t *testing.T) {
		resp := login(t, withEmail, bob, &handlers.AuthRequest{Email: members[1].Email})
		verifyProcessCSP(t, withEmail, members[1].Email, resp.AuthToken)
	})

	t.Run("bad codes and verifiers answer 40802", func(t *testing.T) {
		c := qt.New(t)
		verifier, challenge := testCertPKCE(t)
		failed := func(req *handlers.AuthRequest, pid string) {
			t.Helper()
			err := postProcessAuth0AndExpectError(t, pid, req)
			c.Assert(err.Code, qt.Equals, errors.ErrCSPAuthFailed.Code)
		}
		otherVerifier, _ := testCertPKCE(t)
		failed(&handlers.AuthRequest{CertCode: newCertCode(t, authOnly, alice, challenge), CodeVerifier: otherVerifier}, authOnly)

		reused := newCertCode(t, authOnly, alice, challenge)
		postProcessAuth0(t, authOnly, &handlers.AuthRequest{CertCode: reused, CodeVerifier: verifier})
		failed(&handlers.AuthRequest{CertCode: reused, CodeVerifier: verifier}, authOnly)

		expired := newCertCode(t, authOnly, alice, challenge)
		codeHash := sha256.Sum256([]byte(expired))
		_, err := testDB.DBClient.Database(testDBName).Collection("certAuthAssertions").UpdateOne(context.Background(),
			bson.M{"codeHash": codeHash[:]}, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Second)}})
		c.Assert(err, qt.IsNil)
		failed(&handlers.AuthRequest{CertCode: expired, CodeVerifier: verifier}, authOnly)

		otherProcess := newCertCode(t, withEmail, alice, challenge)
		failed(&handlers.AuthRequest{CertCode: otherProcess, CodeVerifier: verifier}, authOnly)

		wrongContact := newCertCode(t, withEmail, alice, challenge)
		failed(&handlers.AuthRequest{CertCode: wrongContact, CodeVerifier: verifier, Email: "other@example.com"}, withEmail)
		failed(&handlers.AuthRequest{CertCode: wrongContact, CodeVerifier: verifier, Email: members[0].Email}, withEmail)
	})

	t.Run("typed fields or no code on a cert census: 40005, and the code stays unused", func(t *testing.T) {
		c := qt.New(t)
		verifier, challenge := testCertPKCE(t)
		code := newCertCode(t, authOnly, alice, challenge)
		for _, req := range []*handlers.AuthRequest{
			{CertCode: code, CodeVerifier: verifier, NationalID: alice},
			{CertCode: code, CodeVerifier: verifier, Name: "Alice"},
			{CertCode: code},
			{CodeVerifier: verifier},
			{NationalID: alice},
		} {
			err := postProcessAuth0AndExpectError(t, authOnly, req)
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		}
		postProcessAuth0(t, authOnly, &handlers.AuthRequest{CertCode: code, CodeVerifier: verifier})
	})

	t.Run("a code on a census without certAuth: 40005", func(t *testing.T) {
		verifier, challenge := testCertPKCE(t)
		code := newCertCode(t, authOnly, alice, challenge)
		err := postProcessAuth0AndExpectError(t, typed, &handlers.AuthRequest{CertCode: code, CodeVerifier: verifier})
		qt.Assert(t, err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
	})

	t.Run("two concurrent redemptions: exactly one succeeds", func(t *testing.T) {
		verifier, challenge := testCertPKCE(t)
		req := &handlers.AuthRequest{CertCode: newCertCode(t, authOnly, bob, challenge), CodeVerifier: verifier}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range codes {
			wg.Go(func() { _, codes[i] = certStep0(t, authOnly, req) })
		}
		wg.Wait()
		qt.Assert(t, codes, qt.ContentEquals, []int{http.StatusOK, errors.ErrCSPAuthFailed.HTTPstatus})
	})

	t.Run("no PII in the logs of an assertion and a step 0", func(t *testing.T) {
		c := qt.New(t)
		out := captureAPILogs(t, func() {
			login(t, withEmail, alice, &handlers.AuthRequest{Email: members[0].Email})
		})
		c.Assert(out, qt.Contains, "cert assertion created")
		for _, pii := range []string{"1234567", "Alice", "Uno", members[0].Email, "611111111"} {
			c.Assert(out, qt.Not(qt.Contains), pii)
		}
	})
	c.Assert(testAPI.csp.CertAuthEnabled(), qt.IsTrue)
}
