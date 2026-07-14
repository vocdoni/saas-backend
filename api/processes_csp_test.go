package api

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// verifyProcessCSP completes the OTP challenge for a mid-challenge process auth token and
// returns the verified token.
func verifyProcessCSP(t *testing.T, pid, email string, authToken internal.HexBytes) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	otp := extractOTPFromBody(waitForEmail(t, email))
	c.Assert(otp, qt.Not(qt.Equals), "")
	verified := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthChallengeRequest{AuthToken: authToken, AuthData: []string{otp}},
		"processes", pid, "auth", "1")
	c.Assert(verified.AuthToken, qt.Not(qt.HasLen), 0)
	return verified.AuthToken
}

// authProcessCSP runs the full process-scoped CSP auth flow (auth/0 -> OTP -> auth/1) and
// returns the verified auth token.
func authProcessCSP(t *testing.T, pid string, authReq *handlers.AuthRequest) internal.HexBytes {
	t.Helper()
	step0 := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
	qt.Assert(t, step0.AuthToken, qt.Not(qt.HasLen), 0)
	return verifyProcessCSP(t, pid, authReq.Email, step0.AuthToken)
}

// TestProcessCSP exercises the process-scoped CSP handlers (auth/resend/sign/weight/check)
// end to end against a published multi-question process. Question 2 has an eligibility
// subset restricted to the first member, so the second member can authenticate but is not
// eligible for it.
func TestProcessCSP(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// a process whose census authenticates by name+surname with an email OTP; question 2 is
	// restricted to the first member (ids[:1]) via its eligibility subset.
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Weighted = true
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Error))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	// question index 1 is the eligibility-restricted one
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.HasLen, 1)
	openElection := got.Questions[0].UpstreamID       // all census members eligible
	restrictedElection := got.Questions[1].UpstreamID // only the first member eligible
	c.Assert(len(openElection) > 0, qt.IsTrue)
	c.Assert(len(restrictedElection) > 0, qt.IsTrue)

	authReq := func(i int) *handlers.AuthRequest {
		return &handlers.AuthRequest{
			Name:    members[i].Name,
			Surname: members[i].Surname,
			Email:   members[i].Email,
		}
	}
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	// --- first member: authenticate, but keep the mid-challenge token to exercise the
	// unverified-sign gate and the OTP resend before completing verification ---
	step0 := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq(0), "processes", pid, "auth", "0")
	c.Assert(step0.AuthToken, qt.Not(qt.HasLen), 0)

	// an unverified token cannot sign
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: step0.AuthToken,
			ProcessID: openElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")

	// resend the OTP challenge for the mid-challenge token
	resend := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Email: members[0].Email},
		"processes", pid, "auth", "resend")
	c.Assert(resend.AuthToken, qt.Not(qt.HasLen), 0)

	// complete verification
	tok0 := verifyProcessCSP(t, pid, members[0].Email, step0.AuthToken)

	// check: belongs to the process, both questions votable, nothing voted yet
	check0 := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok0}, "processes", pid, "check")
	c.Assert(check0.BelongsToProcess, qt.IsTrue)
	c.Assert(check0.Questions, qt.HasLen, 2)
	for _, q := range check0.Questions {
		c.Assert(q.CanVote, qt.IsTrue, qt.Commentf("member 0 should vote every question"))
		c.Assert(q.HasVoted, qt.IsFalse)
	}

	// weight: member 0 weight is 1 on a weighted census
	weight0 := requestAndParse[handlers.UserWeightResponse](t, http.MethodPost, "",
		&handlers.UserWeightRequest{AuthToken: tok0}, "processes", pid, "weight")
	c.Assert(bytes.Equal(weight0.Weight, big.NewInt(1).Bytes()), qt.IsTrue,
		qt.Commentf("unexpected weight %x", weight0.Weight))

	// sign a ballot for the open election
	sign0 := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok0,
			ProcessID: openElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	c.Assert(sign0.Signature, qt.Not(qt.HasLen), 0)

	// --- second member: authenticates but is not eligible for the restricted question ---
	tok1 := authProcessCSP(t, pid, authReq(1))
	check1 := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok1}, "processes", pid, "check")
	c.Assert(check1.BelongsToProcess, qt.IsTrue)
	for _, q := range check1.Questions {
		if bytes.Equal(q.UpstreamID, restrictedElection) {
			c.Assert(q.CanVote, qt.IsFalse, qt.Commentf("member 1 must not vote the restricted question"))
		} else {
			c.Assert(q.CanVote, qt.IsTrue)
		}
	}

	// signing the restricted election with an ineligible member is rejected
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok1,
			ProcessID: restrictedElection,
			Payload:   hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")

	// a verified token anchored to a different process is rejected on this one
	otherTok := internal.HexBytes(internal.RandomBytes(16))
	otherOID := primitive.NewObjectID()
	otherAnchor := internal.HexBytes(otherOID[:])
	c.Assert(testDB.SetCSPAuth(otherTok, internal.HexBytesFromString(members[0].ID), otherAnchor, ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(otherTok), qt.IsNil)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: otherTok}, "processes", pid, "check")

	// a malformed process id is a client error
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok0}, "processes", "not-a-valid-id", "check")
}
