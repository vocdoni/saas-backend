package api

import (
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// publishedProcess creates and publishes a process over the given member ids and returns its id
// together with its hydrated read response. Question 1 is whole-census, question 2 is restricted to
// the first member (see newVotingProcessRequest).
func publishedProcess(
	t *testing.T, token string, orgAddress common.Address, ids []string,
) (string, apicommon.VotingProcessResponse) {
	t.Helper()
	c := qt.New(t)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = "" // start immediately, so the CSP will sign
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", created.ProcessID,
	)
	return created.ProcessID, got
}

// signAs drives a member through CSP auth and sign for one election, returning the HTTP status.
func signAs(t *testing.T, pid string, member apicommon.OrgMember, election internal.HexBytes) int {
	t.Helper()
	voter := ethereum.SignKeys{}
	qt.Assert(t, voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: member.Name, Surname: member.Surname, Email: member.Email,
	})
	_, code := testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: tok, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes()),
	}, "processes", pid, "sign")
	return code
}

// TestProcessCSPElectionStateGates pins the three refusals that decide whether a voter can obtain a
// signature at all. The CSP is the only place that can enforce them: the signature it issues carries
// no expiry, so a voter could otherwise bank one against a closed election and spend it when the
// chain opens. They are also the refusals most likely to turn away a legitimate voter, which is why
// each is asserted rather than left to the happy path.
//
// The gates read the stored question status and start date, so the status is set directly here — a
// status change driven through the API would be testing the status endpoint instead.
func TestProcessCSPElectionStateGates(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}

	// a draft mints no token: it would outlive the draft and be spendable the moment it publishes,
	// and issuing it would burn the voter's email allowance for nothing
	draftReq := newVotingProcessRequest(orgAddress, ids)
	draftReq.Census.AuthFields = authFields
	draft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, draftReq, processesCreateEndpoint,
	)
	_, code := testRequest(t, http.MethodPost, "", &handlers.AuthRequest{
		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
	}, "processes", draft.ProcessID, "auth", "0")
	c.Assert(code, qt.Equals, http.StatusUnauthorized,
		qt.Commentf("an unpublished process must not authenticate a voter"))

	// a published process that has not opened yet authenticates but does not sign
	futureReq := newVotingProcessRequest(orgAddress, ids) // its StartDate defaults to an hour out
	futureReq.Census.AuthFields = authFields
	future := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, futureReq, processesCreateEndpoint,
	)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", future.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	futureGot := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", future.ProcessID)
	c.Assert(signAs(t, future.ProcessID, members[0], futureGot.Questions[0].UpstreamID),
		qt.Equals, http.StatusUnauthorized,
		qt.Commentf("no signature may be obtainable before the process opens"))

	// a question that is not READY does not sign either
	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0]
	c.Assert(signAs(t, pid, members[0], open.UpstreamID), qt.Equals, http.StatusOK,
		qt.Commentf("the same request must work while the question is READY"))

	c.Assert(testDB.SetQuestionStatus(open.ID, db.QuestionStatusPaused), qt.IsNil)
	// a second member, so the refusal is the pause and not the one-address-per-member rule
	c.Assert(signAs(t, pid, members[1], open.UpstreamID), qt.Equals, http.StatusUnauthorized,
		qt.Commentf("a paused question must not be signed for"))
}

// TestUpdateQuestionCensusReopen is the sharpest case of the #611 endpoint: reopening a restricted
// question adds nobody by name yet multiplies its electorate, while ComputeMaxCensusSize stamped its
// election at exactly the old subset size. Without the resize the CSP signs and the chain rejects.
func TestUpdateQuestionCensusReopen(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	c.Assert(got.Census.Size, qt.Equals, int64(3))

	// question 2 is restricted to the first member, so its election was sized for exactly one voter
	restricted := got.Questions[1]
	c.Assert(restricted.EligibleMemberIDs, qt.DeepEquals, []string{ids[0]})
	elec, err := testAPI.account.Election(restricted.UpstreamID)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(1))

	// a member outside the subset cannot be signed for it
	c.Assert(signAs(t, pid, members[1], restricted.UpstreamID), qt.Equals, http.StatusUnauthorized)

	// reopen it to the whole census: an empty list is "no restriction", not "nobody"
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{}},
		"processes", pid, "questions", restricted.ID.Hex(), "census",
	)
	c.Assert(upd.Eligible, qt.Equals, uint32(0))
	c.Assert(upd.Added, qt.Equals, uint32(0))
	c.Assert(upd.Removed, qt.Equals, uint32(1))
	c.Assert(upd.JobID, qt.Not(qt.Equals), "")

	job := pollJob(t, upd.JobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("resize job error: %s", job.Errors))

	// the election now holds the whole census...
	elec, err = testAPI.account.Election(restricted.UpstreamID)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(3),
		qt.Commentf("reopening must resize the election, not just the stored list"))

	// ...and the previously excluded member is really signed for it
	c.Assert(signAs(t, pid, members[1], restricted.UpstreamID), qt.Equals, http.StatusOK)
}

// TestUpdateQuestionCensusRetriesAnUnenqueuedResize pins that the endpoint is idempotent on what is
// left to do, not on the diff against the stored list.
//
// The list is committed before the resize is enqueued, so an enqueue that fails — a full tx queue
// answers 503 — leaves them disagreeing. Nothing sweeps failed SetProcessCensus jobs, so the retry
// is the whole recovery; a replay that short-circuits on the empty diff makes the state permanent,
// with the CSP signing for voters the election has no room for.
//
// The interrupted request is reproduced by writing the list exactly as the handler does and never
// enqueueing, which is the state it leaves behind.
func TestUpdateQuestionCensusRetriesAnUnenqueuedResize(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	restricted := got.Questions[1]
	c.Assert(restricted.EligibleMemberIDs, qt.DeepEquals, []string{ids[0]})

	elec, err := testAPI.account.Election(restricted.UpstreamID)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(1))

	// the write half of a request whose enqueue then failed
	widened := []string{ids[0], ids[1]}
	won, err := testDB.SetQuestionEligibleMemberIDs(restricted.ID, []string{ids[0]}, widened)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// replaying it diffs to nothing, and must still enqueue the resize
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: widened},
		"processes", pid, "questions", restricted.ID.Hex(), "census",
	)
	c.Assert(upd.Eligible, qt.Equals, uint32(2))
	c.Assert(upd.Added, qt.Equals, uint32(0), qt.Commentf("the replay adds nobody by name"))
	c.Assert(upd.Removed, qt.Equals, uint32(0))
	c.Assert(upd.JobID, qt.Not(qt.Equals), "")

	job := pollJob(t, upd.JobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("resize job error: %s", job.Errors))

	elec, err = testAPI.account.Election(restricted.UpstreamID)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(2),
		qt.Commentf("the retry must resize the election the first attempt never did"))

	// and once there is genuinely nothing left, the replay is a plain 200
	same := requestAndParse[apicommon.UpdateQuestionCensusResponse](t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: widened},
		"processes", pid, "questions", restricted.ID.Hex(), "census")
	c.Assert(same.Eligible, qt.Equals, uint32(2))
	c.Assert(same.JobID, qt.Equals, "")
}

// TestUpdateQuestionCensusRefusesStrippingASignedVoter is the case a diff against the stored list
// cannot see. A question open to the whole census names nobody, so restricting it reports nobody as
// removed — yet every member left out loses the vote, including one already holding a signature the
// chain will accept. The guard therefore asks who has been signed for, not who was named before.
func TestUpdateQuestionCensusRefusesStrippingASignedVoter(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0] // whole census: EligibleMemberIDs is empty
	c.Assert(open.EligibleMemberIDs, qt.HasLen, 0)

	// members[2] is signed for while the question is open to everyone
	c.Assert(signAs(t, pid, members[2], open.UpstreamID), qt.Equals, http.StatusOK)

	// restricting the question to the other two takes the vote from them
	errResp := requestAndExpectError(t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0], ids[1]}},
		"processes", pid, "questions", open.ID.Hex(), "census")
	c.Assert(errResp.Code, qt.Equals, errors.ErrCensusMemberAlreadyVoted.Code)
	data, ok := errResp.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("data: %#v", errResp.Data))
	c.Assert(data["votedMemberIds"], qt.DeepEquals, []any{ids[2]})

	// and the question is still open to the whole census: the refusal happened before the write
	readBack := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(readBack.Questions[0].EligibleMemberIDs, qt.HasLen, 0)

	// a restriction that keeps the signed member is fine
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0], ids[2]}},
		"processes", pid, "questions", open.ID.Hex(), "census",
	)
	c.Assert(upd.Eligible, qt.Equals, uint32(2))

	// once the question is terminal it holds nobody, so the same strip goes through
	c.Assert(testDB.SetQuestionStatus(open.ID, db.QuestionStatusEnded), qt.IsNil)
	upd = requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0], ids[1]}},
		"processes", pid, "questions", open.ID.Hex(), "census",
	)
	c.Assert(upd.Removed, qt.Equals, uint32(1))
}

// TestUpdateQuestionCensusNarrowAndValidate covers the rest of the endpoint's contract: the list is
// the complete desired set, ids must already be census participants, and the write is idempotent.
func TestUpdateQuestionCensusNarrowAndValidate(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0] // whole census

	// narrowing a whole-census question needs no resize: the election already holds everyone
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[1], ids[0]}},
		"processes", pid, "questions", open.ID.Hex(), "census",
	)
	c.Assert(upd.Eligible, qt.Equals, uint32(2))
	c.Assert(upd.JobID, qt.Equals, "")

	// input order is preserved, so a client can diff what it read against its next request
	readBack := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(readBack.Questions[0].EligibleMemberIDs, qt.DeepEquals, []string{ids[1], ids[0]})

	// resending the same list is a no-op, not an error
	upd = requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[1], ids[0]}},
		"processes", pid, "questions", open.ID.Hex(), "census",
	)
	c.Assert(upd.Added, qt.Equals, uint32(0))
	c.Assert(upd.Removed, qt.Equals, uint32(0))

	// duplicates collapse
	upd = requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[1], ids[1], ids[0]}},
		"processes", pid, "questions", open.ID.Hex(), "census",
	)
	c.Assert(upd.Eligible, qt.Equals, uint32(2))

	// an id that is not a census participant is a 400, not a silent drop
	outsider := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{
		MemberNumber: "outsider", Name: "Out", Surname: "Sider", Email: "outsider@example.com",
	})
	var outsiderID string
	for _, m := range outsider {
		if m.Email == "outsider@example.com" {
			outsiderID = m.ID
		}
	}
	c.Assert(outsiderID, qt.Not(qt.Equals), "")
	_, code := testRequest(t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0], outsiderID}},
		"processes", pid, "questions", open.ID.Hex(), "census")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

// TestRemoveProcessCensusContract covers DELETE /processes/{processId}/census beyond the happy path
// the revocation tests already drive: what it refuses, what it reports, and the one thing it must
// never do on chain.
func TestRemoveProcessCensusContract(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// a draft is rebuilt wholesale by PUT /processes, so this endpoint refuses it
	draftReq := newVotingProcessRequest(orgAddress, ids)
	draft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, draftReq, processesCreateEndpoint,
	)
	requestAndAssertError(errors.ErrDuplicateConflict, t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[:1]}, "processes", draft.ProcessID, "census")

	pid, got := publishedProcess(t, token, orgAddress, ids)
	election := got.Questions[0].UpstreamID
	c.Assert(got.Census.Size, qt.Equals, int64(2))
	before, err := testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(before.Census.MaxCensusSize, qt.Equals, uint64(2))

	// an empty list is a no-op, and says so rather than omitting the field
	noop := requestAndParse[apicommon.UpdateProcessCensusResponse](t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: nil}, "processes", pid, "census")
	c.Assert(noop.Removed, qt.Equals, uint32(0))

	// an id that names no participant is reported as what it is: nothing removed
	unknown := requestAndParse[apicommon.UpdateProcessCensusResponse](t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{primitive.NewObjectID().Hex()}},
		"processes", pid, "census")
	c.Assert(unknown.Removed, qt.Equals, uint32(0),
		qt.Commentf("the count must be the rows deleted, not the ids submitted"))

	// removing a real participant shrinks the stored census...
	removed := requestAndParse[apicommon.UpdateProcessCensusResponse](t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{ids[1], ids[1]}},
		"processes", pid, "census")
	c.Assert(removed.Removed, qt.Equals, uint32(1), qt.Commentf("the same id twice is one removal"))
	readBack := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(readBack.Census.Size, qt.Equals, int64(1))

	// ...and leaves the election alone: the chain only accepts growth, so a removal never resizes
	after, err := testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(after.Census.MaxCensusSize, qt.Equals, uint64(2),
		qt.Commentf("a removal must never resize the election"))

	// the body is bounded
	tooMany := make([]string, maxCensusRemoval+1)
	for i := range tooMany {
		tooMany[i] = primitive.NewObjectID().Hex()
	}
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: tooMany}, "processes", pid, "census")

	// and the caller must manage the organization
	requestAndAssertError(errors.ErrUnauthorized, t, http.MethodDelete, testCreateUser(t, "otherpassword123"),
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[:1]}, "processes", pid, "census")
}

// TestResizeEmptiedQuestionsReportsFailures pins that a resize that could not even be planned comes
// back as an error, not as an empty job id. The revocation the resize follows has already
// committed and nothing sweeps a resize that was never enqueued, so an empty id alone would make a
// lost resize read exactly like "no resize was needed" — the handlers put these errors in the
// response body.
func TestResizeEmptiedQuestionsReportsFailures(t *testing.T) {
	c := qt.New(t)
	emptied := []db.VotingProcessQuestion{{
		ID:         primitive.NewObjectID(),
		ProcessID:  primitive.NewObjectID(), // names no process, as after a failed load mid-cascade
		UpstreamID: internal.HexBytes{0xde, 0xad},
	}}
	jobID, errs := testAPI.resizeEmptiedQuestions(common.Address{0x01}, emptied)
	c.Assert(jobID, qt.Equals, "")
	c.Assert(errs, qt.HasLen, 1)
	c.Assert(errs[0], qt.Contains, emptied[0].ID.Hex())
}

// TestProcessCSPRevocation proves the revocation actually revokes: the sign handler re-checks census
// participation, so a member removed from the census stops being signed for even though their token
// is still valid. It also pins the ceiling — a signature already issued is not recalled.
func TestProcessCSPRevocation(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0]

	// members[2] signs while still a participant
	c.Assert(signAs(t, pid, members[2], open.UpstreamID), qt.Equals, http.StatusOK)

	// members[1] is removed from the census before signing
	requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusOK, t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{ids[1]}},
		"processes", pid, "census",
	)
	// they are no longer able to authenticate at all: they are out of the census
	_, code := testRequest(t, http.MethodPost, "", &handlers.AuthRequest{
		Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
	}, "processes", pid, "auth", "0")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// members[0] holds a live verified token, and only then loses their participant row — which is
	// what a group edit does underneath a voter. The sign-time re-check is the only thing that can
	// catch this.
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
	})
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	_, _, err = testDB.RevokeMembersFromCensuses([]string{vp.CensusID.Hex()}, []string{ids[0]})
	c.Assert(err, qt.IsNil)
	_, code = testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: tok, ProcessID: open.UpstreamID, Payload: hex.EncodeToString(voter.Address().Bytes()),
	}, "processes", pid, "sign")
	c.Assert(code, qt.Equals, http.StatusUnauthorized,
		qt.Commentf("a token minted before removal must not keep signing"))

	// the ceiling: members[2]'s consumption survives their removal, because deleting it would let
	// them be signed for a second address and vote twice.
	_, _, err = testDB.RevokeMembersFromCensuses([]string{vp.CensusID.Hex()}, []string{ids[2]})
	c.Assert(err, qt.IsNil)
	consumed, err := testDB.MembersWithUsedCSPProcesses(
		[]internal.HexBytes{open.UpstreamID}, []string{ids[2]},
	)
	c.Assert(err, qt.IsNil)
	c.Assert(consumed, qt.DeepEquals, []string{ids[2]},
		qt.Commentf("the consumption row must survive revocation"))
}

// TestReAddedMemberCannotSignForASecondAddress drives the reason revocation never deletes
// cspTokensStatus: the consumption row pins the one address a member was ever signed for, so a
// member revoked and then re-added cannot be signed for a fresh address — a second address would be
// a second nullifier, a double vote the chain accepts. Re-signing for the original address stays
// allowed, which is the documented vote-overwrite budget.
func TestReAddedMemberCannotSignForASecondAddress(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0]

	// members[1] is signed for address A while a participant
	addressA := ethereum.SignKeys{}
	c.Assert(addressA.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
	})
	_, code := testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: tok, ProcessID: open.UpstreamID, Payload: hex.EncodeToString(addressA.Address().Bytes()),
	}, "processes", pid, "sign")
	c.Assert(code, qt.Equals, http.StatusOK)

	// revoked underneath their signature — the DB call models the release the guard cannot see,
	// exactly as TestProcessCSPRevocation does — then re-added through the census PUT
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	_, _, err = testDB.RevokeMembersFromCensuses([]string{vp.CensusID.Hex()}, []string{ids[1]})
	c.Assert(err, qt.IsNil)
	readd := requestAndParse[apicommon.UpdateProcessCensusResponse](t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{ids[1]}}, "processes", pid, "census")
	c.Assert(readd.Added, qt.Equals, uint32(1))

	// the revocation dropped their session, so they authenticate afresh — and the consumption row
	// that survived it refuses a signature for any address but the pinned one
	tok = authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
	})
	addressB := ethereum.SignKeys{}
	c.Assert(addressB.Generate(), qt.IsNil)
	_, code = testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: tok, ProcessID: open.UpstreamID, Payload: hex.EncodeToString(addressB.Address().Bytes()),
	}, "processes", pid, "sign")
	c.Assert(code, qt.Equals, http.StatusUnauthorized,
		qt.Commentf("a re-added member must not be signed for a second address"))

	// the original address is still signable: that is the vote-overwrite budget, not a new vote
	_, code = testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: tok, ProcessID: open.UpstreamID, Payload: hex.EncodeToString(addressA.Address().Bytes()),
	}, "processes", pid, "sign")
	c.Assert(code, qt.Equals, http.StatusOK)
}

// TestMemberDeletionRefusedForVoter covers the §3 rule and its release: a member the CSP has signed
// for cannot be deleted while the election is running, but can once it has ended.
func TestMemberDeletionRefusedForVoter(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	pid, got := publishedProcess(t, token, orgAddress, ids)
	open := got.Questions[0]
	c.Assert(signAs(t, pid, members[0], open.UpstreamID), qt.Equals, http.StatusOK)

	// refused while the question is still READY
	_, code := testRequest(t, http.MethodDelete, token,
		&apicommon.DeleteMembersRequest{IDs: []string{ids[0]}},
		"organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusConflict)

	// and nothing happened: the member is still there, and still in the census
	member, err := testDB.OrgMember(orgAddress, ids[0])
	c.Assert(err, qt.IsNil)
	c.Assert(member.ID.Hex(), qt.Equals, ids[0])
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	_, err = testDB.CensusParticipant(vp.CensusID.Hex(), ids[0])
	c.Assert(err, qt.IsNil)

	// once every question of the process is terminal the member is released
	for _, q := range got.Questions {
		c.Assert(testDB.SetQuestionStatus(q.ID, db.QuestionStatusEnded), qt.IsNil)
	}
	del := requestAndParse[apicommon.DeleteMembersResponse](t, http.MethodDelete, token,
		&apicommon.DeleteMembersRequest{IDs: []string{ids[0]}},
		"organizations", orgAddress.String(), "members")
	c.Assert(del.Count, qt.Equals, 1)
	_, err = testDB.CensusParticipant(vp.CensusID.Hex(), ids[0])
	c.Assert(err, qt.Equals, db.ErrNotFound)
}

// TestUpdatedMemberDoesNotRejoinRevokedCensus pins that propagation happens on create only.
// AddCensusParticipantsByMemberIDs re-adds anything missing, so running it on an ordinary edit
// would silently undo every revocation.
func TestUpdatedMemberDoesNotRejoinRevokedCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	pid, _ := publishedProcess(t, token, orgAddress, ids)

	requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusOK, t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{ids[1]}},
		"processes", pid, "census",
	)

	// edit the revoked member: this must not put them back in the census
	requestAndParse[apicommon.UpsertOrgMemberResponse](t, http.MethodPut, token, &apicommon.OrgMember{
		ID: ids[1], Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
		MemberNumber: members[1].MemberNumber, Weight: "5",
	}, "organizations", orgAddress.String(), "members")

	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	_, err = testDB.CensusParticipant(vp.CensusID.Hex(), ids[1])
	c.Assert(err, qt.Equals, db.ErrNotFound, qt.Commentf("an edit must not undo a revocation"))

	// and they stay unsignable: the CSP will not even authenticate a non-participant
	_, code := testRequest(t, http.MethodPost, "", &handlers.AuthRequest{
		Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
	}, "processes", pid, "auth", "0")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
}

// TestVotingProcessMutationsRefusedWhilePublishing covers the publish-window guard: while a worker
// holds the process every "is this a draft?" check reads true, so an edit or delete taken on that
// basis lands on a process being put on chain.
func TestVotingProcessMutationsRefusedWhilePublishing(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	pid := created.ProcessID
	oid := objectID(c, pid)

	// claim it directly: no chain work needed to reproduce the window
	won, err := testDB.ClaimVotingProcessForPublish(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)

	edit := newVotingProcessRequest(orgAddress, ids)
	edit.Title = db.MultiLangString{"default": "edited while publishing"}
	_, code := testRequest(t, http.MethodPut, token, edit, "processes", pid)
	c.Assert(code, qt.Equals, http.StatusConflict)

	_, code = testRequest(t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(code, qt.Equals, http.StatusConflict)

	_, code = testRequest(t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0]}},
		"processes", pid, "questions", before.Questions[0].ID.Hex(), "census")
	c.Assert(code, qt.Equals, http.StatusConflict)

	// nothing changed
	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(after.Title, qt.DeepEquals, before.Title)
	c.Assert(after.Questions[0].ID, qt.Equals, before.Questions[0].ID)

	// releasing the claim lets the same requests through
	c.Assert(testDB.ClearVotingProcessPublishing(oid), qt.IsNil)
	_, code = testRequest(t, http.MethodPut, token, edit, "processes", pid)
	c.Assert(code, qt.Equals, http.StatusOK)
}

func objectID(c *qt.C, hexID string) primitive.ObjectID {
	oid, err := primitive.ObjectIDFromHex(hexID)
	c.Assert(err, qt.IsNil)
	return oid
}

// TestGroupAdditionGrowsPublishedCensus proves the additive half end to end: a member added to the
// group behind a published census becomes a real voter, election headroom included.
func TestGroupAdditionGrowsPublishedCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	// a group holding the first two members, and a process whose census is built from it
	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, &apicommon.CreateOrganizationMemberGroupRequest{
			Title: "voters", Description: "group census", MemberIDs: ids[:2],
		}, "organizations", orgAddress.String(), "groups",
	)

	req := newVotingProcessRequest(orgAddress, ids[:2])
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname},
		GroupID:     group.ID,
	}
	req.Questions = req.Questions[:1] // one whole-census question is enough here
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID
	elec, err := testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(2))

	// the third member cannot vote yet
	_, code := testRequest(t, http.MethodPost, "", &handlers.AuthRequest{
		Name: members[2].Name, Surname: members[2].Surname, Email: members[2].Email,
	}, "processes", pid, "auth", "0")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// adding them to the group must reach the live census
	upd := requestAndParse[apicommon.UpdateOrganizationMemberGroupResponse](
		t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{
			AddMembers: []string{ids[2]},
		}, "organizations", orgAddress.String(), "groups", group.ID,
	)
	c.Assert(upd.Errors, qt.HasLen, 0)
	c.Assert(upd.CensusJobIDs, qt.HasLen, 1)

	resize := pollJob(t, upd.CensusJobIDs[0])
	c.Assert(resize.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("resize job error: %s", resize.Errors))

	elec, err = testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(3))

	// and the added member is really signed for by the CSP
	c.Assert(signAs(t, pid, members[2], election), qt.Equals, http.StatusOK)
}

// TestGroupCensusGuards pins the ordering of the removal guard: a refused removal must leave the
// member in the group *and* in the census. Refusing after the group write is the failure mode this
// whole cascade exists to remove.
func TestGroupCensusGuards(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, &apicommon.CreateOrganizationMemberGroupRequest{
			Title: "voters", Description: "group census", MemberIDs: ids,
		}, "organizations", orgAddress.String(), "groups",
	)

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname},
		GroupID:     group.ID,
	}
	req.Questions = req.Questions[:1]
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(signAs(t, pid, members[0], got.Questions[0].UpstreamID), qt.Equals, http.StatusOK)

	// removing that member from the group is refused...
	_, code := testRequest(t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{
		RemoveMembers: []string{ids[0]},
	}, "organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(code, qt.Equals, http.StatusConflict)

	// ...and left them in the group AND in the census
	stored, err := testDB.OrganizationMemberGroup(group.ID, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.MemberIDs, qt.Contains, ids[0])
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	_, err = testDB.CensusParticipant(vp.CensusID.Hex(), ids[0])
	c.Assert(err, qt.IsNil)

	// deleting the whole group is refused for the same reason
	_, code = testRequest(t, http.MethodDelete, token, nil,
		"organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(code, qt.Equals, http.StatusConflict)
	_, err = testDB.OrganizationMemberGroup(group.ID, orgAddress)
	c.Assert(err, qt.IsNil)
}

// TestDeleteMembersIsOrgScoped pins that the member ids a caller submits are scoped to their own
// organization before anything acts on them. The delete is org-scoped already, but the guard and
// the revocation cascade resolve censuses from the ids alone: unscoped, an admin of one
// organization could revoke the voters of another, and would be answered 409 naming a member they
// cannot even see.
func TestDeleteMembersIsOrgScoped(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")

	// the organization under attack: a published process whose first member has been signed for
	victim := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, victim, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, victim, newOrgMembers(3)...)
	ids := memberIDs(members)
	pid, got := publishedProcess(t, token, victim, ids)
	open := got.Questions[0]
	c.Assert(signAs(t, pid, members[0], open.UpstreamID), qt.Equals, http.StatusOK)

	// a second organization, of the same user, asks to delete the first one's members
	attacker := testCreateOrganization(t, token)
	setOrganizationSubscription(t, attacker, mockEssentialPlan.ID)

	del := requestAndParse[apicommon.DeleteMembersResponse](t, http.MethodDelete, token,
		&apicommon.DeleteMembersRequest{IDs: ids},
		"organizations", attacker.String(), "members")
	c.Assert(del.Count, qt.Equals, 0,
		qt.Commentf("foreign ids delete nothing, and must not be answered 409 either"))

	// the victim's census is untouched, member by member
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	for _, id := range ids {
		_, err := testDB.CensusParticipant(vp.CensusID.Hex(), id)
		c.Assert(err, qt.IsNil, qt.Commentf("member %s was revoked by another organization", id))
	}
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(census.Size, qt.Equals, int64(3))

	// and its voters are still signed for
	c.Assert(signAs(t, pid, members[1], open.UpstreamID), qt.Equals, http.StatusOK)
}

// TestRemoveProcessCensusIsOrgScoped is TestDeleteMembersIsOrgScoped for the other door into the
// same cascade. DELETE /processes/{processId}/census reaches RevokeMembersFromCensuses directly
// rather than through DeleteOrgMembers, so it does not inherit the scoping that lives there.
//
// Two of the three revocation writes are census-scoped, which makes a foreign id inert in them and
// hides the hole. The third deletes CSP auth sessions by userid alone, so an unscoped id logs a
// voter of another organization out mid-election — asserted here on an already-issued auth token,
// since a fresh auth would silently paper over the deletion.
func TestRemoveProcessCensusIsOrgScoped(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")

	victim := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, victim, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, victim, newOrgMembers(2)...)
	ids := memberIDs(members)
	victimPID, victimGot := publishedProcess(t, token, victim, ids)
	open := victimGot.Questions[0]

	// the victim's voter authenticates but has not spent the token yet
	victimToken := authProcessCSP(t, victimPID, &handlers.AuthRequest{
		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
	})

	// the attacker owns a published process of their own, so they are a legitimate caller of this
	// endpoint — they simply name ids that are not theirs
	attacker := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, attacker, mockEssentialPlan.ID)
	attackerMembers := postOrgMembers(t, token, attacker, newOrgMembers(2)...)
	attackerPID, _ := publishedProcess(t, token, attacker, memberIDs(attackerMembers))

	removed := requestAndParse[apicommon.UpdateProcessCensusResponse](t, http.MethodDelete, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids},
		"processes", attackerPID, "census")
	c.Assert(removed.Removed, qt.Equals, uint32(0),
		qt.Commentf("foreign ids remove nothing, and must not be answered 409 either"))

	// the victim's census is untouched, member by member
	vp, err := testDB.VotingProcess(objectID(c, victimPID))
	c.Assert(err, qt.IsNil)
	for _, id := range ids {
		_, err := testDB.CensusParticipant(vp.CensusID.Hex(), id)
		c.Assert(err, qt.IsNil, qt.Commentf("member %s was revoked by another organization", id))
	}

	// and the token issued before the attack still signs: the auth session survived
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	_, code := testRequest(t, http.MethodPost, "", &handlers.SignRequest{
		AuthToken: victimToken, ProcessID: open.UpstreamID, Payload: hex.EncodeToString(voter.Address().Bytes()),
	}, "processes", victimPID, "sign")
	c.Assert(code, qt.Equals, http.StatusOK,
		qt.Commentf("another organization deleted this voter's CSP auth session"))
}
