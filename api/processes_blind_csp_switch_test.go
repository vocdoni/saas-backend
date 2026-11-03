package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// setDisableBlindCSP flips the operator switch on the shared test API for the rest of the test.
// It mutates shared testAPI state; it relies on package api tests never running in parallel — do not use with t.Parallel.
func setDisableBlindCSP(t *testing.T, on bool) {
	t.Helper()
	previous := testAPI.disableBlindCSP
	testAPI.disableBlindCSP = on
	t.Cleanup(func() { testAPI.disableBlindCSP = previous })
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
	assertAnonymousVotingUnavailable(t, http.MethodPost, token, nil, "processes", anonymousDraft, "publish")
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
