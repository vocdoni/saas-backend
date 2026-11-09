package api

import (
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// setDisableBlindCSP flips the operator switch on the shared test CSP for the rest of the test.
// It mutates shared testAPI state; it relies on package api tests never running in parallel — do not use with t.Parallel.
func setDisableBlindCSP(t *testing.T, on bool) {
	t.Helper()
	previous := testAPI.csp.BlindCSPDisabled()
	testAPI.csp.SetBlindCSPDisabled(on)
	t.Cleanup(func() { testAPI.csp.SetBlindCSPDisabled(previous) })
}

// assertPublishNotReady asserts the publish is refused by the preflight, listing the switch's problem.
func assertPublishNotReady(t *testing.T, jwt, pid string) {
	t.Helper()
	apiErr := requestAndExpectError(t, http.MethodPost, jwt, nil, "processes", pid, "publish")
	qt.Assert(t, apiErr.Code, qt.Equals, errors.ErrMalformedBody.Code)
	qt.Assert(t, apiErr.Error(), qt.Contains, "process is not ready to publish")
	qt.Assert(t, apiErr.Error(), qt.Contains, anonymousVotingUnavailable)
}

// assertAnonymousVotingUnavailable asserts the request is refused with the switch's 400.
func assertAnonymousVotingUnavailable(t *testing.T, method, jwt string, jsonBody any, urlPath ...string) {
	t.Helper()
	apiErr := requestAndExpectError(t, method, jwt, jsonBody, urlPath...)
	qt.Assert(t, apiErr.Code, qt.Equals, errors.ErrInvalidData.Code)
	qt.Assert(t, apiErr.Error(), qt.Contains, anonymousVotingUnavailable)
}

// TestProcessBlindCSPSwitch checks the operator switch that temporarily stops offering anonymous
// voting: with it on, an anonymous census cannot be created, set on a draft or published (even
// from a draft stored earlier), while non-anonymous processes keep working.
func TestProcessBlindCSPSwitch(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(2)...))

	anonymousReq := newVotingProcessRequest(orgAddress, ids)
	anonymousReq.StartDate = ""
	anonymousReq.Census.Anonymous = true
	plainReq := newVotingProcessRequest(orgAddress, ids)
	plainReq.StartDate = ""

	// switch off (the default): an anonymous draft is accepted and reported as publishable
	anonymousDraft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, anonymousReq, processesCreateEndpoint).ProcessID
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", anonymousDraft, "validation")
	c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("problems: %v", validation.Errors))
	plainDraft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, plainReq, processesCreateEndpoint).ProcessID

	setDisableBlindCSP(t, true)

	// create with an anonymous census is refused
	assertAnonymousVotingUnavailable(t, http.MethodPost, token, anonymousReq, processesCreateEndpoint)

	// census validation preflight with an anonymous spec is refused
	censusValidateReq := apicommon.ValidateProcessCensusRequest{
		OrgAddress: orgAddress.Bytes(),
		Census:     apicommon.CensusSpec{TwoFaFields: anonymousReq.Census.TwoFaFields, MemberIDs: ids, Anonymous: true},
	}
	assertAnonymousVotingUnavailable(t, http.MethodPost, token, censusValidateReq, processesCensusValidateEndpoint)

	// updating a draft to an anonymous census is refused and leaves the draft as it was
	assertAnonymousVotingUnavailable(t, http.MethodPut, token, anonymousReq, "processes", plainDraft)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", plainDraft)
	c.Assert(got.Census.Anonymous, qt.IsFalse)

	// the anonymous draft stored earlier is reported as not publishable, and its publish is refused
	validation = requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", anonymousDraft, "validation")
	c.Assert(validation.Valid, qt.IsFalse)
	c.Assert(validation.Errors, qt.Contains, anonymousVotingUnavailable)
	assertPublishNotReady(t, token, anonymousDraft)
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", anonymousDraft)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Census.Anonymous, qt.IsTrue)

	// non-anonymous processes are unaffected: a new one is created, the draft is updated, and both publish
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, plainReq, processesCreateEndpoint).ProcessID
	requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPut, token, plainReq, "processes", plainDraft)
	for _, pid := range []string{created, plainDraft} {
		validation = requestAndParse[apicommon.VotingProcessValidateResponse](
			t, http.MethodGet, token, nil, "processes", pid, "validation")
		c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("problems: %v", validation.Errors))
		job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
		got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.Published, qt.IsTrue)
		c.Assert(got.Census.Anonymous, qt.IsFalse)
	}
}

// TestProcessBlindCSPSwitchResumesPartialPublish checks that an anonymous process some of whose
// questions are already on chain still resumes its publish with anonymous voting off: it cannot be
// edited or deleted, so refusing the resume would leave it half published.
func TestProcessBlindCSPSwitchResumesPartialPublish(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(2)...))

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.Anonymous = true
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	_, questions, err := testDB.ProcessWithQuestions(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 2)

	// simulate a publish that mined the first question's election and then failed
	mined := internal.HexBytes(randomProcessID())
	c.Assert(testDB.SetQuestionPublished(questions[0].ID, mined, "url", db.QuestionStatusReady), qt.IsNil)

	setDisableBlindCSP(t, true)

	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("problems: %v", validation.Errors))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Published, qt.IsTrue)
	c.Assert(got.Census.Anonymous, qt.IsTrue)
	c.Assert(got.Questions, qt.HasLen, 2)
	c.Assert(got.Questions[0].UpstreamID.String(), qt.Equals, mined.String())
	c.Assert(got.Questions[1].UpstreamID, qt.Not(qt.HasLen), 0)
}

// TestProcessBlindCSPSwitchStopsBlindSigning checks that with anonymous voting off the blind rounds
// of an already published anonymous process are refused, the plain sign endpoints answer as before,
// and blind signing works again once the switch is off.
func TestProcessBlindCSPSwitchStopsBlindSigning(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	voterMember := members[1]

	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Anonymous = true
	req.Questions = req.Questions[:1]
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID

	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: voterMember.Name, Surname: voterMember.Surname, Email: voterMember.Email,
	})
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	setDisableBlindCSP(t, true)
	// publishing it again answers as for any published process: 200 with its id, nothing enqueued
	again := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(again.ProcessID, qt.Equals, pid)
	assertAnonymousVotingUnavailable(t, http.MethodPost, "",
		&handlers.BlindPointRequest{AuthToken: tok, Elections: []internal.HexBytes{election}},
		"processes", pid, "blind-point")
	assertAnonymousVotingUnavailable(t, http.MethodPost, "",
		&handlers.BlindSignRequest{AuthToken: tok, Ballots: []handlers.BlindSignBallot{
			{UpstreamID: election, BlindedMessage: internal.HexBytes{0x01}},
		}}, "processes", pid, "blind-sign")
	// the plain sign endpoint keeps its own answer for an anonymous census
	apiErr := requestAndExpectError(t, http.MethodPost, "",
		&handlers.SignRequest{AuthToken: tok, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes())},
		"processes", pid, "sign")
	c.Assert(apiErr.Code, qt.Equals, errors.ErrMalformedBody.Code)
	c.Assert(apiErr.Error(), qt.Not(qt.Contains), anonymousVotingUnavailable)

	// back on: the voter completes the blind flow
	setDisableBlindCSP(t, false)
	proof := testBlindSignBallot(t, pid, tok, election, voter.Address().Bytes())
	c.Assert(proof.GetCa(), qt.Not(qt.IsNil))
}

// waitSentEmails polls the organization's sent-email counter, which is bumped asynchronously once a
// challenge is delivered, until it reaches want, and returns its counters.
func waitSentEmails(t *testing.T, orgAddress common.Address, want int) db.OrganizationCounters {
	t.Helper()
	var counters db.OrganizationCounters
	for range 50 {
		org, err := testDB.Organization(orgAddress)
		qt.Assert(t, err, qt.IsNil)
		counters = org.Counters
		if counters.SentEmails >= want {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	qt.Assert(t, counters.SentEmails, qt.Equals, want)
	return counters
}

// TestProcessBlindCSPSwitchStopsAuthChallenges checks that with anonymous voting off an anonymous
// process sends no auth challenge (step 0 and resend are refused and the org's sent counters stay
// put), a non-anonymous process still sends its own, and voters already verified keep check and
// sign-info. Once the switch is off the anonymous process sends challenges again.
func TestProcessBlindCSPSwitchStopsAuthChallenges(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	authReq := func(i int) *handlers.AuthRequest {
		return &handlers.AuthRequest{Name: members[i].Name, Surname: members[i].Surname, Email: members[i].Email}
	}

	newRequest := func(anonymous bool) *apicommon.CreateVotingProcessRequest {
		req := newVotingProcessRequest(orgAddress, memberIDs(members))
		req.StartDate = ""
		req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
		req.Census.Anonymous = anonymous
		req.Questions = req.Questions[:1]
		return req
	}
	anonymousPID, _ := publishProcessRequest(t, token, newRequest(true))
	plainPID, _ := publishProcessRequest(t, token, newRequest(false))

	// switch off: member 0 gets a challenge it does not answer yet, member 1 completes the auth
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	sent := org.Counters.SentEmails
	pending := requestAndParse[handlers.AuthResponse](
		t, http.MethodPost, "", authReq(0), "processes", anonymousPID, "auth", "0").AuthToken
	c.Assert(pending, qt.Not(qt.HasLen), 0)
	waitForEmail(t, members[0].Email) // drained, so the next OTP read for member 0 is the next challenge
	verified := authProcessCSP(t, anonymousPID, authReq(1))
	before := waitSentEmails(t, orgAddress, sent+2)

	setDisableBlindCSP(t, true)

	// the anonymous process sends nothing: step 0 and resend are refused
	for i := range members {
		assertAnonymousVotingUnavailable(t, http.MethodPost, "", authReq(i), "processes", anonymousPID, "auth", "0")
	}
	assertAnonymousVotingUnavailable(t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: pending, Email: members[0].Email},
		"processes", anonymousPID, "auth", "resend")

	// a voter verified earlier still reads their status and sign info
	check := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: verified}, "processes", anonymousPID, "check")
	c.Assert(check.BelongsToProcess, qt.IsTrue)
	requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: verified}, "processes", anonymousPID, "sign-info")

	// the non-anonymous process still sends its challenge, and it is the only send counted
	c.Assert(authProcessCSP(t, plainPID, authReq(0)), qt.Not(qt.HasLen), 0)
	after := waitSentEmails(t, orgAddress, before.SentEmails+1)
	c.Assert(after.SentSMS, qt.Equals, before.SentSMS)

	// back on: the anonymous process sends challenges again
	setDisableBlindCSP(t, false)
	resent := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: pending, Email: members[0].Email},
		"processes", anonymousPID, "auth", "resend")
	c.Assert(resent.AuthToken, qt.DeepEquals, pending)
	waitForEmail(t, members[0].Email)
	time.Sleep(cspNotificationCoolDownTime) // the cooldown between new tokens of one member
	c.Assert(authProcessCSP(t, anonymousPID, authReq(0)), qt.Not(qt.HasLen), 0)
	waitSentEmails(t, orgAddress, after.SentEmails+2)
}
