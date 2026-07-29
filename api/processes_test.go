package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/types"
)

// newVotingProcessRequest builds a 2-question (singlechoice + multichoice) create request
// with the given census member ids and an eligibility subset on the second question.
func newVotingProcessRequest(
	orgAddress common.Address, memberIDs []string,
) *apicommon.CreateVotingProcessRequest {
	yesNo := []db.Choice{
		{Title: db.MultiLangString{"default": "Yes"}, Value: 0}, //nolint:goconst
		{Title: db.MultiLangString{"default": "No"}, Value: 1},
	}
	return &apicommon.CreateVotingProcessRequest{
		OrgAddress: orgAddress.Bytes(),
		Census: apicommon.CensusSpec{
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
			MemberIDs:   memberIDs,
		},
		Title:     db.MultiLangString{"default": "Test process"},
		StartDate: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		EndDate:   time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		Questions: []apicommon.VotingProcessQuestionRequest{
			{
				Title:     db.MultiLangString{"default": "Q1"},
				Choices:   yesNo,
				Type:      db.VotingTypeSingleChoice,
				TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
			},
			{
				Title:       db.MultiLangString{"default": "Q2"},
				Choices:     yesNo,
				Type:        db.VotingTypeMultiChoice,
				TypeSetup:   db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
				Eligibility: &apicommon.EligibilitySpec{MemberIDs: memberIDs[:1]}, // only the first member
			},
		},
	}
}

func TestVotingProcessAuthoring(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// create a 2-question draft
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	c.Assert(created.ProcessID, qt.Not(qt.Equals), "")
	pid := created.ProcessID

	// read it back (full, hydrated questions)
	got := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid,
	)
	c.Assert(got.ID, qt.Equals, pid)
	c.Assert(got.Published, qt.IsFalse)
	// chainId is exposed so clients sign votes against the right chain (vote sigs are chain-id-bound)
	c.Assert(got.ChainID, qt.Equals, testAPI.account.ChainID())
	c.Assert(got.ChainID, qt.Not(qt.Equals), "")
	c.Assert(got.Questions, qt.HasLen, 2)
	c.Assert(got.Questions[0].Type, qt.Equals, db.VotingTypeSingleChoice)
	c.Assert(got.Questions[1].Type, qt.Equals, db.VotingTypeMultiChoice)
	// eligibility subset is public and restricted to the first member on Q2
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.HasLen, 1)
	c.Assert(got.Questions[0].EligibleMemberIDs, qt.HasLen, 0) // Q1 = all census members
	// census config is exposed, member list is not; size reflects the 2 seeded members
	c.Assert(got.Census.TwoFaFields, qt.HasLen, 1)
	c.Assert(got.Census.Size, qt.Equals, int64(2))

	// list contains the process with its questions
	list := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, adminToken, nil, fmt.Sprintf("processes?orgAddress=%s&limit=100", orgAddress.Hex()),
	)
	c.Assert(len(list.Processes) >= 1, qt.IsTrue)
	found := false
	for _, p := range list.Processes {
		if p.ID == pid {
			found = true
			c.Assert(p.Questions, qt.HasLen, 2)
			c.Assert(p.ChainID, qt.Equals, testAPI.account.ChainID())
		}
	}
	c.Assert(found, qt.IsTrue)

	// validate (dry-run): a complete draft is ready to publish
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "validation",
	)
	c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("errors: %v", validation.Errors))

	// the public single-question read is voter-facing: a draft (unpublished) process is not
	// readable without auth (404), so draft content and eligibility are not exposed.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil,
		"processes", pid, "questions", got.Questions[0].ID.Hex())
}

func TestVotingProcessUpdate(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	pid := created.ProcessID

	// update the title while still a draft
	upd := newVotingProcessRequest(orgAddress, ids)
	upd.Title = db.MultiLangString{"default": "Updated title"}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, upd, "processes", pid)

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(got.Title["default"], qt.Equals, "Updated title")
}

func TestVotingProcessAuthoringErrors(t *testing.T) {
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// zero questions -> 400
	empty := newVotingProcessRequest(orgAddress, ids)
	empty.Questions = nil
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, empty, processesCreateEndpoint)

	// eligibility member not in the census -> 400
	badElig := newVotingProcessRequest(orgAddress, ids)
	badElig.Questions[1].Eligibility = &apicommon.EligibilitySpec{MemberIDs: []string{"000000000000000000000000"}}
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, badElig, processesCreateEndpoint)

	// a multichoice question with an out-of-range maxChoices -> 400 (0 is unbounded, >choices
	// is nonsensical). Question index 1 is the multichoice one with 2 choices.
	zeroMax := newVotingProcessRequest(orgAddress, ids)
	zeroMax.Questions[1].TypeSetup.MaxChoices = 0
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, zeroMax, processesCreateEndpoint)

	tooManyMax := newVotingProcessRequest(orgAddress, ids)
	tooManyMax.Questions[1].TypeSetup.MaxChoices = 5 // only 2 choices
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, tooManyMax, processesCreateEndpoint)

	// a question with neither a type nor a ballotProtocol -> 400
	noShape := newVotingProcessRequest(orgAddress, ids)
	noShape.Questions[0].Type = ""
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, noShape, processesCreateEndpoint)

	// a question with an unsupported type (and no ballotProtocol) -> 400
	badType := newVotingProcessRequest(orgAddress, ids)
	badType.Questions[0].Type = "quadratic"
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, badType, processesCreateEndpoint)

	// every create above failed, so no orphaned draft was left behind (they roll back)
	count, err := testDB.CountVotingProcesses(orgAddress, db.AllProcesses)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, count, qt.Equals, int64(0))

	// a raw ballotProtocol override satisfies the ballot-shape requirement even without a type
	rawProto := newVotingProcessRequest(orgAddress, ids)
	rawProto.Questions[0].Type = ""
	rawProto.Questions[0].BallotProtocol = &db.BallotProtocol{MaxCount: 1, MaxValue: 1}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, rawProto, processesCreateEndpoint)

	// a user with no role on the org -> 401
	otherToken := testCreateUser(t, "otherpassword123")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, otherToken,
		newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

	// a draft is hidden from an anonymous caller (the single read is public, but drafts 404 for
	// non-managers so their existence isn't revealed).
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", created.ProcessID)
	// ...and from an authenticated user with no role on the org.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, otherToken, nil, "processes", created.ProcessID)
}

// TestVotingProcessEmptyCensusRejected verifies the preflight rejects a process whose census has
// no voters (auth-only census, no members, no eligibility subsets) synchronously — the dry-run is
// invalid and publish returns 400 instead of a 202 that fails opaquely in the worker.
func TestVotingProcessEmptyCensusRejected(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, minimalVotingProcessRequest(orgAddress), processesCreateEndpoint,
	)

	val := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", created.ProcessID, "validation",
	)
	c.Assert(val.Valid, qt.IsFalse)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
}

// minimalVotingProcessRequest builds a 1-question, member-less draft (empty census, no
// eligibility subset) — the smallest request that passes validation, used where the test does
// not need members.
func minimalVotingProcessRequest(orgAddress common.Address) *apicommon.CreateVotingProcessRequest {
	return &apicommon.CreateVotingProcessRequest{
		OrgAddress: orgAddress.Bytes(),
		Census:     apicommon.CensusSpec{TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}},
		Title:      db.MultiLangString{"default": "key proc"}, //nolint:goconst
		StartDate:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		EndDate:    time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		Questions: []apicommon.VotingProcessQuestionRequest{{
			Title: db.MultiLangString{"default": "Q1"},
			Choices: []db.Choice{
				{Title: db.MultiLangString{"default": "Yes"}, Value: 0}, //nolint:goconst
				{Title: db.MultiLangString{"default": "No"}, Value: 1},
			},
			Type:      db.VotingTypeSingleChoice,
			TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
		}},
	}
}

// TestVotingProcessAPIKeyAuth verifies the new /processes write routes honour API-key
// (integrator) auth: a voting:write key can create a process for its managed org, and a key
// without that scope is refused (403).
func TestVotingProcessAPIKeyAuth(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "integratorpass123")
	orgAddr := testCreateOrganization(t, token)
	// the integrator's plan governs the managed org's draft quota
	setOrganizationSubscription(t, orgAddr, mockEssentialPlan.ID)
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// mint a voting:write key (managed:write is needed to create the managed org)
	createBody := &apicommon.CreateAPIKeyRequest{Label: "voting", Scopes: []string{ScopeManagedWrite, ScopeVotingWrite}}
	data, code := testRequest(t, http.MethodPost, token, createBody, "integrator", "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var created apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &created), qt.IsNil)
	apiKey := created.Secret

	// create a managed org with the key; the key owner is its admin
	mbody := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://md.example"},
	}
	managed := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, apiKey, mbody, "integrator", "organizations")
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})

	// the voting:write key can create a /processes draft for the managed org
	proc := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, apiKey, minimalVotingProcessRequest(managed.Address), processesCreateEndpoint,
	)
	c.Assert(proc.ProcessID, qt.Not(qt.Equals), "")

	// the voting:write key reads its managed org's DRAFT via the now-public single read (optionalManager
	// resolves the key to its admin user); anonymous callers get 404 (the draft's existence is hidden).
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, apiKey, nil, "processes", proc.ProcessID)
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", proc.ProcessID)

	// a key without voting:write is refused (403)
	noScopeBody := &apicommon.CreateAPIKeyRequest{Label: "noscope", Scopes: []string{ScopeManagedRead}}
	data, code = testRequest(t, http.MethodPost, token, noScopeBody, "integrator", "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var noScope apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &noScope), qt.IsNil)
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, noScope.Secret,
		minimalVotingProcessRequest(managed.Address), processesCreateEndpoint)
	// a key without voting:write is treated as anonymous on the public read, so the draft is 404.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, noScope.Secret, nil, "processes", proc.ProcessID)
}

// TestVotingProcessPublishPreflight verifies the publish-readiness dry-run now catches plan
// denials that used to only surface as an async job failure: a process whose duration exceeds
// the plan limit is reported invalid by GET .../check AND rejected 400 by publish (synchronously,
// never enqueued).
func TestVotingProcessPublishPreflight(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID) // MaxDuration 90 days
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// a draft with a 120-day duration (over the plan's 90-day MaxDuration)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	req.EndDate = time.Now().Add(120 * 24 * time.Hour).UTC().Format(time.RFC3339)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID

	// the dry-run reports it not ready (previously it passed the structural-only check)
	val := requestAndParse[apicommon.VotingProcessValidateResponse](t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(val.Valid, qt.IsFalse)
	c.Assert(len(val.Errors) > 0, qt.IsTrue)

	// publish rejects it synchronously with 400 (not a 202 that later fails as an opaque job)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token, nil, "processes", pid, "publish")
}

// TestVotingProcessUpdateNoCensusOrphan verifies that editing a draft does not accumulate
// orphaned censuses: after a create and an update the org has exactly one census.
func TestVotingProcessUpdateNoCensusOrphan(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)

	after, err := testDB.CensusesByOrg(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after, qt.HasLen, 1)

	// update the draft twice; each edit re-resolves the census and must reap the previous one
	for i := 0; i < 2; i++ {
		upd := newVotingProcessRequest(orgAddress, ids)
		upd.Title = db.MultiLangString{"default": "edited"}
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, upd, "processes", created.ProcessID)
		censuses, err := testDB.CensusesByOrg(orgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(censuses, qt.HasLen, 1, qt.Commentf("edit %d orphaned a census", i))
	}
}

// TestQuestionResultsFromElection checks the chain->QuestionResults mapping directly (no chain):
// the full tally matrix is preserved verbatim, so single-choice (one field) and multi-choice (one
// field per choice) shapes are both represented losslessly; maxVoters comes from the election census
// (nil-safe); an untallied election yields nil results.
func TestQuestionResultsFromElection(t *testing.T) {
	c := qt.New(t)
	bi := func(n uint64) *types.BigInt { return new(types.BigInt).SetUint64(n) }

	// multi-choice: one row per choice, each [notSelected, selected].
	multi := questionResultsFromElection(&dvoteapi.Election{
		ElectionSummary: dvoteapi.ElectionSummary{
			VoteCount: 3, FinalResults: true,
			Results: [][]*types.BigInt{{bi(1), bi(2)}, {bi(1), bi(2)}, {bi(2), bi(1)}},
		},
		Census: &dvoteapi.ElectionCensus{MaxCensusSize: 5},
	})
	c.Assert(multi.VoteCount, qt.Equals, uint64(3))
	c.Assert(multi.FinalResults, qt.IsTrue)
	c.Assert(multi.MaxVoters, qt.Equals, uint64(5))
	c.Assert(multi.Results, qt.DeepEquals, [][]string{{"1", "2"}, {"1", "2"}, {"2", "1"}})

	// single-choice: one row of value buckets; nil census is tolerated (maxVoters 0).
	single := questionResultsFromElection(&dvoteapi.Election{
		ElectionSummary: dvoteapi.ElectionSummary{Results: [][]*types.BigInt{{bi(4), bi(6)}}},
	})
	c.Assert(single.Results, qt.DeepEquals, [][]string{{"4", "6"}})
	c.Assert(single.MaxVoters, qt.Equals, uint64(0))

	// no tally published yet: results absent.
	c.Assert(questionResultsFromElection(&dvoteapi.Election{}).Results, qt.IsNil)
}

// TestVotingProcessResults verifies the public per-question results endpoint: a draft has no
// results (404), and a published process returns one entry per question with its on-chain status.
func TestVotingProcessResults(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)

	// a draft has no on-chain results yet
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", created.ProcessID, "results")

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// published (no votes yet): one entry per question, voteCount 0, tally not final, buckets "0".
	// maxVoters is the per-question eligible count: Q1 is whole-census (2 members), Q2 restricts
	// eligibility to the first member (1). The results matrix has one row per ballot field: the
	// singlechoice Q1 is one field (one row of value buckets), the multichoice Q2 is one field per
	// choice (a row per choice), so the two shapes differ.
	res := requestAndParse[apicommon.VotingProcessResultsResponse](
		t, http.MethodGet, "", nil, "processes", created.ProcessID, "results",
	)
	c.Assert(res.ID, qt.Equals, created.ProcessID)
	c.Assert(res.Questions, qt.HasLen, 2)
	for _, q := range res.Questions {
		c.Assert(q.QuestionID, qt.Not(qt.Equals), "")
		c.Assert(len(q.UpstreamID) > 0, qt.IsTrue)
		c.Assert(q.VoteCount, qt.Equals, uint64(0))
		c.Assert(q.FinalResults, qt.IsFalse)
	}
	// Q1 singlechoice: one field, two value buckets. Q2 multichoice: one field per choice (two), each
	// [notSelected, selected].
	c.Assert(res.Questions[0].Results, qt.DeepEquals, [][]string{{"0", "0"}})
	c.Assert(res.Questions[1].Results, qt.DeepEquals, [][]string{{"0", "0"}, {"0", "0"}})
	c.Assert(res.Questions[0].MaxVoters, qt.Equals, uint64(2)) // whole census
	c.Assert(res.Questions[1].MaxVoters, qt.Equals, uint64(1)) // eligibility subset of one

	// live results: the inline reads now surface the tally for a published (non-RESULTS) question too,
	// with finalResults=false — the old gate would have left results nil until RESULTS.
	info := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
	c.Assert(info.Questions, qt.HasLen, 2)
	for _, q := range info.Questions {
		c.Assert(q.Results, qt.Not(qt.IsNil), qt.Commentf("question %s missing live results", q.ID.Hex()))
		c.Assert(q.Results.FinalResults, qt.IsFalse)
	}
	pub := requestAndParse[apicommon.PublicQuestionResponse](
		t, http.MethodGet, "", nil, "processes", created.ProcessID, "questions", info.Questions[0].ID.Hex())
	c.Assert(pub.Results, qt.Not(qt.IsNil))
	c.Assert(pub.Results.FinalResults, qt.IsFalse)
}

// TestVotingProcessPublicReads verifies the process list and single read are public for published
// processes (with per-question eligibleMemberIds stripped for non-managers), while drafts and the
// eligibility are visible only to a manager/admin of the org. It also covers the optional published
// filter on the list: it narrows the caller's default view, and asking for drafts without a
// manager/admin role is refused rather than silently returning an empty list.
func TestVotingProcessPublicReads(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	otherToken := testCreateUser(t, "otherpass123") // a user with no role in the org

	// a published process (Q2 = multichoice, restricts eligibility to the first member)
	pubReq := newVotingProcessRequest(orgAddress, ids)
	pubReq.StartDate = ""
	published := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, pubReq, processesCreateEndpoint)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", published.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish error: %s", job.Errors))

	// a draft, kept unpublished
	draft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

	// eligibility of the restricted (multichoice) question in a hydrated response
	restrictedEligibility := func(qs []db.VotingProcessQuestion) []string {
		for _, q := range qs {
			if q.Type == db.VotingTypeMultiChoice {
				return q.EligibleMemberIDs
			}
		}
		return nil
	}
	hasProcess := func(ps []apicommon.VotingProcessResponse, id string) bool {
		for _, p := range ps {
			if p.ID == id {
				return true
			}
		}
		return false
	}

	// anonymous single read of the PUBLISHED process: 200, eligibility stripped; manager sees it.
	anon := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", published.ProcessID)
	c.Assert(anon.Questions, qt.HasLen, 2)
	c.Assert(restrictedEligibility(anon.Questions), qt.HasLen, 0)
	mgr := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", published.ProcessID)
	c.Assert(restrictedEligibility(mgr.Questions), qt.HasLen, 1)

	// the DRAFT is hidden (404) from anonymous and a non-manager user; the manager sees it with eligibility.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", draft.ProcessID)
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, otherToken, nil, "processes", draft.ProcessID)
	draftMgr := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", draft.ProcessID)
	c.Assert(restrictedEligibility(draftMgr.Questions), qt.HasLen, 1)

	// anonymous list: only the published process, eligibility stripped.
	listURL := fmt.Sprintf("processes?orgAddress=%s&limit=100", orgAddress.Hex())
	anonList := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, "", nil, listURL)
	c.Assert(hasProcess(anonList.Processes, published.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(anonList.Processes, draft.ProcessID), qt.IsFalse)
	for _, p := range anonList.Processes {
		c.Assert(restrictedEligibility(p.Questions), qt.HasLen, 0)
	}

	// manager list: both the published process and the draft.
	mgrList := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil, listURL)
	c.Assert(hasProcess(mgrList.Processes, published.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(mgrList.Processes, draft.ProcessID), qt.IsTrue)

	// published=false narrows the manager view to drafts, published=true to published processes. The
	// unfiltered manager list above is the third case: omitting the param still returns both.
	draftsURL := listURL + "&published=false"
	mgrDrafts := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil, draftsURL)
	c.Assert(hasProcess(mgrDrafts.Processes, draft.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(mgrDrafts.Processes, published.ProcessID), qt.IsFalse)
	mgrPublished := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, token, nil, listURL+"&published=true")
	c.Assert(hasProcess(mgrPublished.Processes, published.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(mgrPublished.Processes, draft.ProcessID), qt.IsFalse)

	// the value is parsed with strconv.ParseBool, so "0" filters drafts exactly like "false".
	mgrZero := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil, listURL+"&published=0")
	c.Assert(hasProcess(mgrZero.Processes, draft.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(mgrZero.Processes, published.ProcessID), qt.IsFalse)

	// drafts are manager-only: anonymous and a non-member user are refused (401) rather than being
	// served an empty list, which would read as "this org has no drafts".
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, "", nil, draftsURL)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, otherToken, nil, draftsURL)

	// published=true stays public.
	anonPublished := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, "", nil, listURL+"&published=true")
	c.Assert(hasProcess(anonPublished.Processes, published.ProcessID), qt.IsTrue)
	c.Assert(hasProcess(anonPublished.Processes, draft.ProcessID), qt.IsFalse)

	// an unparseable value is malformed (400) whatever the credentials — the parse precedes the
	// manager check, so a bad param is never reported as an auth failure.
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, token, nil, listURL+"&published=notabool")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, listURL+"&published=notabool")

	// a zero orgAddress passes IsHexAddress but is malformed -> 400 (not a 500 from the db layer).
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil,
		"processes?orgAddress=0x0000000000000000000000000000000000000000")
}

// TestVotingProcessPublicQuestionCensus verifies the public single-question read of a PUBLISHED
// process includes the parent census config (the auth policy) but never the eligibility member
// list, and that a restricted question's eligibleMemberIds is not serialized.
func TestVotingProcessPublicQuestionCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// public read (no token) of question 2 (the eligibility-restricted one): census config present,
	// eligibleMemberIds NOT exposed. Assert against the raw JSON so a re-added field can't slip in.
	raw, code := testRequest(t, http.MethodGet, "", nil,
		"processes", created.ProcessID, "questions", got.Questions[1].ID.Hex())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", raw))
	var q apicommon.PublicQuestionResponse
	c.Assert(json.Unmarshal(raw, &q), qt.IsNil)
	c.Assert(q.ID, qt.Equals, got.Questions[1].ID)
	c.Assert(q.Census.TwoFaFields, qt.Contains, db.OrgMemberTwoFaFieldEmail)
	c.Assert(strings.Contains(string(raw), "eligibleMemberIds"), qt.IsFalse,
		qt.Commentf("public read leaked eligibleMemberIds: %s", raw))
}

// TestVotingProcessParticipant verifies the participant endpoint validates the process and
// participant id (mirrors the bundle equivalent).
func TestVotingProcessParticipant(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)

	// a draft (unpublished) process is a public read: not revealed -> 404
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil,
		"processes", created.ProcessID, "participants", ids[0])

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// once published, a valid process + participant id resolves (public, 200, placeholder body)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, "", nil, "processes", created.ProcessID, "participants", ids[0])
	// a non-existent process is 404
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil,
		"processes", primitive.NewObjectID().Hex(), "participants", ids[0])
	// a malformed process id is 400
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "processes", "not-an-id", "participants", ids[0])
}

// TestVotingProcessStalePublishReclaim verifies the publishing marker is reclaimable once stale:
// a fresh marker blocks a second claim, but a marker older than PublishStaleAfter is reclaimed
// (and surfaced by StaleVotingProcesses), so a crash mid-publish cannot lock a process forever.
func TestVotingProcessStalePublishReclaim(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	oid, err := primitive.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)

	won, err := testDB.ClaimVotingProcessForPublish(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	// a fresh marker blocks a concurrent claim
	won, err = testDB.ClaimVotingProcessForPublish(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsFalse)

	// make the marker stale: it is now reclaimable and reported for reconciliation
	restore := db.PublishStaleAfter
	db.PublishStaleAfter = -time.Minute
	defer func() { db.PublishStaleAfter = restore }()

	stale, err := testDB.StaleVotingProcesses()
	c.Assert(err, qt.IsNil)
	found := false
	for _, id := range stale {
		if id == oid {
			found = true
		}
	}
	c.Assert(found, qt.IsTrue)

	won, err = testDB.ClaimVotingProcessForPublish(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue, qt.Commentf("a stale marker must be reclaimable"))
}

// TestVotingProcessConcurrentUpdates is the regression test for issue #614: two overlapping draft
// updates left a question stranded, and publish then turned the orphan into a real on-chain
// election. The old write path deleted every question and re-inserted with fresh ids, so an
// interleaved pair of writers could delete rows the other had not inserted yet.
func TestVotingProcessConcurrentUpdates(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	pid := created.ProcessID

	before := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(before.Questions, qt.HasLen, 2)

	// fire overlapping updates the way the wizard did: an auto-save on blur racing the submit. Each
	// request is a complete, valid draft; whichever lands last is the one that should survive.
	const writers = 6
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			req := newVotingProcessRequest(orgAddress, ids)
			req.Title = db.MultiLangString{"default": fmt.Sprintf("concurrent update %d", i)}
			// tolerate whatever status the race produces; the assertion is on the stored state
			_, _ = testRequest(t, http.MethodPut, adminToken, req, "processes", pid)
		})
	}
	wg.Wait()

	after := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(after.Questions, qt.HasLen, 2,
		qt.Commentf("concurrent updates must not leave a stranded question: %d found", len(after.Questions)))

	// the surviving pair is one slot each, in order, with distinct ids reused from the original draft
	c.Assert(after.Questions[0].Type, qt.Equals, db.VotingTypeSingleChoice)
	c.Assert(after.Questions[1].Type, qt.Equals, db.VotingTypeMultiChoice)
	c.Assert(after.Questions[0].ID, qt.Not(qt.Equals), after.Questions[1].ID)
	c.Assert(after.Questions[0].ID, qt.Equals, before.Questions[0].ID,
		qt.Commentf("question ids must survive a draft edit"))
	c.Assert(after.Questions[1].ID, qt.Equals, before.Questions[1].ID)
}

// TestVotingProcessConcurrentUpdatesVaryingLengths races draft updates of differing lengths, so the
// writers contend over question slots that do not exist yet — the insert path
// TestVotingProcessConcurrentUpdates never reaches, since every writer there sends the same two
// questions. Whatever the race leaves behind, the stored set is never both inconsistent and
// publishable: a lost row makes the process refuse to publish until the draft is saved again.
func TestVotingProcessConcurrentUpdatesVaryingLengths(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	pid := created.ProcessID

	const writers = 9
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			req := newVotingProcessRequest(orgAddress, ids)
			// 2, 3 or 4 questions: the extra slots are inserted rather than replaced
			for range i % 3 {
				req.Questions = append(req.Questions, req.Questions[0])
			}
			req.Title = db.MultiLangString{"default": fmt.Sprintf("varying update %d", i)}
			// tolerate whatever status the race produces; the assertions are on the stored state
			_, _ = testRequest(t, http.MethodPut, adminToken, req, "processes", pid)
		})
	}
	wg.Wait()

	after := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	seen := make(map[string]bool, len(after.Questions))
	for i := range after.Questions {
		id := after.Questions[i].ID.Hex()
		c.Assert(seen[id], qt.IsFalse, qt.Commentf("question %s stored twice (issue #614)", id))
		seen[id] = true
	}

	// the fail-safe property end to end: the process is never reported ready to publish while its
	// stored question set is inconsistent, and the only tolerated problem is that inconsistency.
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "validation")
	if !validation.Valid {
		c.Assert(fmt.Sprint(validation.Errors), qt.Contains, "stored questions do not match the process",
			qt.Commentf("unexpected validation errors: %v", validation.Errors))
	}
}

// TestVotingProcessConditionalUpdate covers the optimistic-concurrency token of PUT /processes/{id}:
// an update carrying the updatedAt the client read applies, the same token replayed after that write
// is stale and rejected with 409, and an update with no token keeps working unconditionally.
func TestVotingProcessConditionalUpdate(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	pid := created.ProcessID

	read := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(read.UpdatedAt, qt.Not(qt.Equals), "")
	seen := read.UpdatedAt

	// the token the client just read still matches: the update applies
	req := newVotingProcessRequest(orgAddress, ids)
	req.Title = db.MultiLangString{"default": "conditional edit"}
	req.UpdatedAt = seen
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, req, "processes", pid)

	refetched := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(refetched.Title["default"], qt.Equals, "conditional edit")
	c.Assert(refetched.UpdatedAt, qt.Not(qt.Equals), seen)

	// replaying the now-stale token is refused rather than overwriting the newer state
	stale := newVotingProcessRequest(orgAddress, ids)
	stale.Title = db.MultiLangString{"default": "should not land"}
	stale.UpdatedAt = seen
	requestAndAssertError(errors.ErrStaleUpdate, t, http.MethodPut, adminToken, stale, "processes", pid)

	unchanged := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(unchanged.Title["default"], qt.Equals, "conditional edit")

	// the same request with the refreshed token succeeds
	stale.UpdatedAt = unchanged.UpdatedAt
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, stale, "processes", pid)

	// and omitting the token keeps the previous unconditional behaviour
	noToken := newVotingProcessRequest(orgAddress, ids)
	noToken.Title = db.MultiLangString{"default": "unconditional"}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, noToken, "processes", pid)
	final := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(final.Title["default"], qt.Equals, "unconditional")

	// a malformed token is a 400, not a silent opt-out
	bad := newVotingProcessRequest(orgAddress, ids)
	bad.UpdatedAt = "not-a-timestamp"
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, adminToken, bad, "processes", pid)
}
