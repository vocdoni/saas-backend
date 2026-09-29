package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
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
	c.Assert(groups, qt.Not(qt.Contains), bson.NilObjectID.Hex())
}

// censusIssues is the data of a 400 from the census pre-flight or a census build: the ids of the
// members that make the census unusable.
type censusIssues struct {
	Duplicates  []string `json:"duplicates"`
	MissingData []string `json:"missingData"`
}

// expectCensusIssues sends the request, asserts an invalid-data 400 and returns the members it names.
func expectCensusIssues(t *testing.T, method, jwt string, body any, urlPath ...string) censusIssues {
	t.Helper()
	resp, code := testRequest(t, method, jwt, body, urlPath...)
	qt.Assert(t, code, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))
	var parsed struct {
		Code int          `json:"code"`
		Data censusIssues `json:"data"`
	}
	qt.Assert(t, json.Unmarshal(resp, &parsed), qt.IsNil, qt.Commentf("response: %s", resp))
	qt.Assert(t, parsed.Code, qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("response: %s", resp))
	return parsed.Data
}

// TestProcessCensusMissingData covers members with empty login fields. The pre-flight
// (POST /processes/census/validation) reports them as missing data, never as duplicates, and a
// create/update whose group holds several of them missing the same data (so they would share login
// credentials) is refused with a 400 naming them, rather than failing the census build with a 500.
func TestProcessCensusMissingData(t *testing.T) {
	token := testCreateUser(t, "missingdatapass123")
	nameNationalID := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsNationalID}
	emailOnly := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	// login is the part of a member the cases vary; everything else keeps newOrgMembers' unique values.
	type login struct{ name, nationalID, email string }

	for _, tc := range []struct {
		name    string
		auth    db.OrgMemberAuthFields
		twoFa   db.OrgMemberTwoFaFields
		members []login
		missing []int // what the pre-flight reports, as indexes into members
		refused []int // what create/update refuses; empty means the census builds
	}{
		{
			name:  "email 2FA with members that have no email",
			twoFa: emailOnly,
			members: []login{
				{name: "A", email: ""},
				{name: "B", email: ""},
				{name: "C", email: ""},
				{name: "D", email: "d@example.com"},
			},
			missing: []int{0, 1, 2},
			refused: []int{0, 1, 2},
		},
		{
			name: "same auth field missing on two members",
			auth: nameNationalID,
			members: []login{
				{name: "Twin", nationalID: "", email: "twin1@example.com"},
				{name: "Twin", nationalID: "", email: "twin2@example.com"},
				{name: "Twin", nationalID: "X1", email: "twin3@example.com"},
			},
			missing: []int{0, 1},
			refused: []int{0, 1},
		},
		{
			name: "missing data without a clash still builds",
			auth: nameNationalID,
			members: []login{
				{name: "Solo", nationalID: "", email: "solo@example.com"},
				{name: "Other", nationalID: "X1", email: "other@example.com"},
			},
			missing: []int{0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			orgAddress := testCreateOrganization(t, token)
			setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

			toAdd := newOrgMembers(len(tc.members))
			for i, l := range tc.members {
				toAdd[i].Name, toAdd[i].NationalID, toAdd[i].Email = l.name, l.nationalID, l.email
			}
			byNumber := make(map[string]string, len(toAdd))
			for _, m := range postOrgMembers(t, token, orgAddress, toAdd...) {
				byNumber[m.MemberNumber] = m.ID
			}
			ids := make([]string, len(toAdd))
			for i, m := range toAdd {
				ids[i] = byNumber[m.MemberNumber]
				c.Assert(ids[i], qt.Not(qt.Equals), "")
			}
			pick := func(idx []int) []string {
				out := make([]string, 0, len(idx))
				for _, i := range idx {
					out = append(out, ids[i])
				}
				return out
			}

			group := postGroup(t, token, orgAddress, ids...)
			byGroup := apicommon.CensusSpec{AuthFields: tc.auth, TwoFaFields: tc.twoFa, GroupID: group.ID}
			byMembers := apicommon.CensusSpec{AuthFields: tc.auth, TwoFaFields: tc.twoFa, MemberIDs: ids}

			// the pre-flight reports them as missing data, not duplicates, over the group and the id list
			for _, spec := range []apicommon.CensusSpec{byGroup, byMembers} {
				issues := expectCensusIssues(t, http.MethodPost, token,
					&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec},
					"processes", "census", "validation")
				c.Assert(issues.Duplicates, qt.HasLen, 0)
				c.Assert(issues.MissingData, qt.ContentEquals, pick(tc.missing))
			}

			req := minimalVotingProcessRequest(orgAddress)
			req.Census = byGroup
			if len(tc.refused) == 0 {
				// the census holds them; the member with missing data just cannot log in
				requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, req, processesCreateEndpoint)
				return
			}

			// create, and the update of an existing draft, refuse the clashing members by id
			draft := requestAndParse[apicommon.CreateVotingProcessResponse](
				t, http.MethodPost, token, minimalVotingProcessRequest(orgAddress), processesCreateEndpoint)
			issues := expectCensusIssues(t, http.MethodPost, token, req, processesCreateEndpoint)
			c.Assert(issues.Duplicates, qt.HasLen, 0)
			c.Assert(issues.MissingData, qt.ContentEquals, pick(tc.refused))
			issues = expectCensusIssues(t, http.MethodPut, token, req, "processes", draft.ProcessID)
			c.Assert(issues.Duplicates, qt.HasLen, 0)
			c.Assert(issues.MissingData, qt.ContentEquals, pick(tc.refused))

			// a refused build leaves nothing behind: the group backs no census, and the draft keeps
			// the empty census it was created with
			stored, err := testDB.OrganizationMemberGroup(group.ID, orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(stored.CensusIDs, qt.HasLen, 0)
			got := requestAndParse[apicommon.VotingProcessResponse](
				t, http.MethodGet, token, nil, "processes", draft.ProcessID)
			c.Assert(got.Census.Size, qt.Equals, int64(0))
		})
	}
}
