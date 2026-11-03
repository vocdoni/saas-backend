package api

import (
	"encoding/hex"
	"net/http"
	"testing"

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
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
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
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
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
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
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
