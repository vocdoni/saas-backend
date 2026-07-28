package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// TestValidateProcessCensus exercises POST /processes/census/validation over the whole org, an
// explicit memberIds subset (db.CheckMembersFields), and the duplicate-detection / auth paths.
func TestValidateProcessCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	authNameSurname := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}

	validate := func(jwt string, spec apicommon.CensusSpec) int {
		_, code := testRequest(t, http.MethodPost, jwt,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec},
			"processes", "census", "validation")
		return code
	}

	// whole org (no group, no memberIds): 3 distinct members validate cleanly.
	c.Assert(validate(token, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusOK)
	// explicit memberIds subset (distinct) also validates.
	c.Assert(validate(token, apicommon.CensusSpec{
		AuthFields: authNameSurname, MemberIDs: []string{members[0].ID, members[1].ID},
	}), qt.Equals, http.StatusOK)

	// add a member that duplicates member[0] on name+surname.
	dup := apicommon.OrgMember{
		MemberNumber: "DUP1", Name: members[0].Name, Surname: members[0].Surname,
		Email: "dup1@example.com", Phone: "+34699999991", Password: "pw", NationalID: "DNIDUP1", BirthDate: "1980-01-01",
	}
	all := postOrgMembers(t, token, orgAddress, dup)
	var dupID string
	for _, m := range all {
		if m.Email == dup.Email {
			dupID = m.ID
		}
	}
	c.Assert(dupID, qt.Not(qt.Equals), "")

	// whole org now contains a name+surname duplicate → 400.
	c.Assert(validate(token, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusBadRequest)
	// the memberIds subset that includes the duplicate is also rejected.
	c.Assert(validate(token, apicommon.CensusSpec{
		AuthFields: authNameSurname, MemberIDs: []string{members[0].ID, dupID},
	}), qt.Equals, http.StatusBadRequest)

	// no auth/2FA fields at all → 400.
	c.Assert(validate(token, apicommon.CensusSpec{}), qt.Equals, http.StatusBadRequest)

	// a user with no role for the org cannot validate.
	other := testCreateUser(t, "otherpass123")
	c.Assert(validate(other, apicommon.CensusSpec{AuthFields: authNameSurname}), qt.Equals, http.StatusUnauthorized)
}

// TestUpdateProcessCensus publishes a process, adds a new org member to its census via
// PUT /processes/{processId}/census, and verifies the member becomes eligible (CSP sign) and the
// on-chain maxCensusSize was raised.
func TestUpdateProcessCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	// census = the first two members; members[2] is an org member not yet in the census.
	req := newVotingProcessRequest(orgAddress, ids[:2])
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	openElection := got.Questions[0].UpstreamID // question 0 has no eligibility subset → whole census
	c.Assert(len(openElection) > 0, qt.IsTrue)
	// the published process reports its census size (== on-chain maxCensusSize for whole-census questions)
	c.Assert(got.Census.Size, qt.Equals, int64(2))

	// maxCensusSize on chain starts at the census size (2).
	elec, err := testAPI.account.Election(openElection)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census, qt.Not(qt.IsNil))
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(2))

	// add the third member to the census.
	upd := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{ids[2]}},
		"processes", pid, "census")
	c.Assert(upd.Added, qt.Equals, uint32(1))
	c.Assert(upd.JobID, qt.Not(qt.Equals), "")

	// the on-chain maxCensusSize bump completes.
	censusJob := pollJob(t, upd.JobID)
	c.Assert(censusJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("census job error: %s", censusJob.Errors))
	elec, err = testAPI.account.Election(openElection)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(3), qt.Commentf("maxCensusSize should have grown to 3"))

	// the process census response now reports the grown size.
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(3))

	// the newly added member can now authenticate and sign the open election.
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[2].Name, Surname: members[2].Surname, Email: members[2].Email,
	})
	sign := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok, ProcessID: openElection, Payload: hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	c.Assert(sign.Signature, qt.Not(qt.HasLen), 0)
}

// TestProcessesCensusGroupID verifies the census groupId round-trips: a process created from an org
// member group reports that group on both process reads and on the org census list, so a client can
// restore the group a draft targeted. An organization-wide census reports no group at all — the
// field must be absent, never a zero object id serialized as 24 zeros.
func TestProcessesCensusGroupID(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "groupcensuspass123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	group := postGroup(t, token, orgAddress, memberIDs(members)...)

	// a process whose census is built from a group, and one over the whole organization.
	groupReq := minimalVotingProcessRequest(orgAddress)
	groupReq.Census.GroupID = group.ID
	grouped := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, groupReq, processesCreateEndpoint,
	)
	orgWide := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, minimalVotingProcessRequest(orgAddress), processesCreateEndpoint,
	)

	// GET /processes/{id}: the group round-trips, the org-wide census carries no group.
	got := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", grouped.ProcessID,
	)
	c.Assert(got.Census.GroupID, qt.Equals, group.ID)
	c.Assert(got.Census.Size, qt.Equals, int64(len(members)))
	plain := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", orgWide.ProcessID,
	)
	c.Assert(plain.Census.GroupID, qt.Equals, "")

	// GET /processes: the list builds its census through the same helper, so it must agree with the
	// single read (totalWeight, set per-handler, already diverges here — groupId must not).
	list := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil,
		fmt.Sprintf("processes?orgAddress=%s&limit=100", orgAddress.Hex()))
	listed := make(map[string]apicommon.CensusSpec, len(list.Processes))
	for _, p := range list.Processes {
		listed[p.ID] = p.Census
	}
	c.Assert(listed[grouped.ProcessID].GroupID, qt.Equals, group.ID)
	c.Assert(listed[orgWide.ProcessID].GroupID, qt.Equals, "")

	// the org census list reports the same, and must not invent a zero group for the org-wide census.
	censuses := requestAndParse[apicommon.OrganizationCensuses](t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "censuses")
	groups := make([]string, 0, len(censuses.Censuses))
	for _, census := range censuses.Censuses {
		groups = append(groups, census.GroupID)
	}
	c.Assert(groups, qt.Contains, group.ID)
	c.Assert(groups, qt.Contains, "")
	c.Assert(groups, qt.Not(qt.Contains), primitive.NilObjectID.Hex())
}

// TestUpdateQuestionCensus adds a member to a published question's eligibility subset and verifies
// the member really can vote afterwards: the CSP signs for them AND the question's on-chain
// maxCensusSize grew to fit them. Without the size bump the CSP would sign a ballot the chain then
// rejects, so both halves matter.
func TestUpdateQuestionCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// question 1 is restricted to ids[0]; question 0 is open to the whole census.
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	qid := got.Questions[1].ID.Hex()
	election := got.Questions[1].UpstreamID
	c.Assert(len(election) > 0, qt.IsTrue)
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids[:1])

	// the subset question's election was sized for exactly its one eligible member.
	elec, err := testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census, qt.Not(qt.IsNil))
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(1))

	// members[1] is already a census participant, so it only has to become eligible.
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids},
		"processes", pid, "questions", qid, "census")
	c.Assert(upd.Added, qt.Equals, 1)
	c.Assert(upd.Removed, qt.Equals, 0)
	c.Assert(upd.Eligible, qt.Equals, 2)
	c.Assert(upd.JobID, qt.Not(qt.Equals), "")

	sizeJob := pollJob(t, upd.JobID)
	c.Assert(sizeJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("size job error: %s", sizeJob.Errors))
	elec, err = testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(2),
		qt.Commentf("the question's maxCensusSize should have grown to fit the new member"))

	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids)

	// replaying the same list changes nothing and enqueues no job.
	replay := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids},
		"processes", pid, "questions", qid, "census")
	c.Assert(replay.Added, qt.Equals, 0)
	c.Assert(replay.Eligible, qt.Equals, 2)
	c.Assert(replay.JobID, qt.Equals, "")

	// the newly eligible member can now authenticate and sign that question's election.
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[1].Name, Surname: members[1].Surname, Email: members[1].Email,
	})
	sign := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	c.Assert(sign.Signature, qt.Not(qt.HasLen), 0)

	// members[1] has now voted this question, so they can no longer be dropped from it. The error
	// names them so the caller knows which ids to put back.
	apiErr := requestAndExpectError(t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:1]},
		"processes", pid, "questions", qid, "census")
	c.Assert(apiErr.Code, qt.Equals, errors.ErrQuestionEligibilityVoted.Code)
	data, isMap := apiErr.Data.(map[string]any)
	c.Assert(isMap, qt.IsTrue, qt.Commentf("error data: %#v", apiErr.Data))
	c.Assert(data["votedMemberIds"], qt.DeepEquals, []any{ids[1]})

	// and the refusal changed nothing
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids)

	// the member who never voted can be dropped, and that alone never goes on chain — the election
	// keeps the headroom it already has.
	drop := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[1:]},
		"processes", pid, "questions", qid, "census")
	c.Assert(drop.Removed, qt.Equals, 1)
	c.Assert(drop.Added, qt.Equals, 0)
	c.Assert(drop.Eligible, qt.Equals, 1)
	c.Assert(drop.JobID, qt.Equals, "")
	elec, err = testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(2), qt.Commentf("a removal must not resize the election"))

	// question 0 is open to the whole census: narrowing it is a removal for everyone left out, and
	// is allowed here because nobody has voted it. Reopening it takes eligibility from nobody.
	openQID := got.Questions[0].ID.Hex()
	narrow := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:1]},
		"processes", pid, "questions", openQID, "census")
	c.Assert(narrow.Eligible, qt.Equals, 1)
	reopen := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: nil},
		"processes", pid, "questions", openQID, "census")
	c.Assert(reopen.Eligible, qt.Equals, 0)
	c.Assert(reopen.Removed, qt.Equals, 1)

	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions[0].EligibleMemberIDs, qt.HasLen, 0)
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids[1:])
}

// TestUpdateQuestionCensusDraft verifies that while the process is still a draft the eligible list
// is simply replaced — members may be removed and swapped, and nothing goes on chain.
func TestUpdateQuestionCensusDraft(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	subsetQID := got.Questions[1].ID.Hex()
	openQID := got.Questions[0].ID.Hex()

	// swap the eligible member for the other one: one added, one removed, no job.
	upd := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[1:]},
		"processes", pid, "questions", subsetQID, "census")
	c.Assert(upd.Added, qt.Equals, 1)
	c.Assert(upd.Removed, qt.Equals, 1)
	c.Assert(upd.Eligible, qt.Equals, 1)
	c.Assert(upd.JobID, qt.Equals, "")

	// a draft may also be opened back up to the whole census, and restricted from it.
	requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: nil},
		"processes", pid, "questions", subsetQID, "census")
	requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:1]},
		"processes", pid, "questions", openQID, "census")

	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions[0].EligibleMemberIDs, qt.DeepEquals, ids[:1])
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.HasLen, 0)
}

// TestUpdateQuestionCensusRejects covers the guards: a published question's electorate may only
// grow, ids must already be census participants, and the caller must manage the organization.
func TestUpdateQuestionCensusRejects(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	// census = the first two members; members[2] is an org member outside the census.
	req := newVotingProcessRequest(orgAddress, ids[:2])
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	subsetQID := got.Questions[1].ID.Hex() // restricted to ids[0]

	for _, tc := range []struct {
		name     string
		qid      string
		jwt      string
		body     *apicommon.UpdateQuestionCensusRequest
		expected errors.Error
	}{
		{
			"member is not in the process census", subsetQID, token,
			&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{ids[0], ids[2]}},
			errors.ErrInvalidData,
		},
		{
			"question belongs to another process", primitive.NewObjectID().Hex(), token,
			&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:1]},
			errors.ErrProcessNotFound,
		},
		{
			"caller does not manage the organization", subsetQID, testCreateUser(t, "otherpassword123"),
			&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:2]},
			errors.ErrUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestAndAssertError(tc.expected, t, http.MethodPut, tc.jwt, tc.body,
				"processes", pid, "questions", tc.qid, "census")
		})
	}

	// a malformed question id is a 400, not a 404
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:1]},
		"processes", pid, "questions", "not-an-objectid", "census")

	// none of the rejections changed the stored eligibility
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Questions[0].EligibleMemberIDs, qt.HasLen, 0)
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids[:1])
}

// TestUpdateQuestionCensusReopen covers the transition that adds nobody by name yet can multiply the
// electorate: a published question restricted to a subset, reopened to the whole census. Its election
// was sized for the subset, so it must be resized — keying the resize off the count of added members
// would skip it here, and the newly eligible members would be signed by the CSP only for the chain to
// reject their ballots.
func TestUpdateQuestionCensusReopen(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	ids := memberIDs(members)

	// a census of three; question 1 is restricted to a single member, so its election is published
	// with room for exactly one voter while the census has three.
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(3))
	qid := got.Questions[1].ID.Hex()
	election := got.Questions[1].UpstreamID
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids[:1])

	elec, err := testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(1),
		qt.Commentf("a subset question is published sized for its subset"))

	// reopening it to the whole census: nobody is named, so nothing is "added", but two more members
	// become eligible and the election has no room for them.
	reopen := requestAndParseWithAssertCode[apicommon.UpdateQuestionCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: nil},
		"processes", pid, "questions", qid, "census")
	c.Assert(reopen.Eligible, qt.Equals, 0)
	c.Assert(reopen.Added, qt.Equals, 0)
	c.Assert(reopen.Removed, qt.Equals, 1)
	c.Assert(reopen.JobID, qt.Not(qt.Equals), "")

	sizeJob := pollJob(t, reopen.JobID)
	c.Assert(sizeJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("size job error: %s", sizeJob.Errors))
	elec, err = testAPI.account.Election(election)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(3),
		qt.Commentf("reopening to the whole census must resize the election to fit it"))

	// a member who was never in the subset can now authenticate and be signed for the question
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name: members[2].Name, Surname: members[2].Surname, Email: members[2].Email,
	})
	sign := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.SignRequest{
			AuthToken: tok, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes()),
		}, "processes", pid, "sign")
	c.Assert(sign.Signature, qt.Not(qt.HasLen), 0)
}
