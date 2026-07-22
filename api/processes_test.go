package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
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

	// two choices marking openValue -> 400 (at most one memo-open choice per question)
	twoOpen := newVotingProcessRequest(orgAddress, ids)
	twoOpen.Questions[0].Choices = []db.Choice{
		{Title: db.MultiLangString{"default": "Yes"}, Value: 0, OpenValue: true},
		{Title: db.MultiLangString{"default": "No"}, Value: 1, OpenValue: true},
	}
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, twoOpen, processesCreateEndpoint)

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

	// unauthenticated read of a process -> 401
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, "", nil, "processes", created.ProcessID)
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

	// a key without voting:write is refused (403)
	noScopeBody := &apicommon.CreateAPIKeyRequest{Label: "noscope", Scopes: []string{ScopeManagedRead}}
	data, code = testRequest(t, http.MethodPost, token, noScopeBody, "integrator", "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var noScope apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &noScope), qt.IsNil)
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, noScope.Secret,
		minimalVotingProcessRequest(managed.Address), processesCreateEndpoint)
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

	// published: one results entry per question, each with its on-chain election id and status
	res := requestAndParse[apicommon.VotingProcessResultsResponse](
		t, http.MethodGet, "", nil, "processes", created.ProcessID, "results",
	)
	c.Assert(res.ID, qt.Equals, created.ProcessID)
	c.Assert(res.Questions, qt.HasLen, 2)
	for _, q := range res.Questions {
		c.Assert(q.QuestionID, qt.Not(qt.Equals), "")
		c.Assert(len(q.UpstreamID) > 0, qt.IsTrue)
		c.Assert(q.Status, qt.Not(qt.Equals), "")
	}
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
