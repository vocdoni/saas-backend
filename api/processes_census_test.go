package api

import (
	"encoding/hex"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
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
