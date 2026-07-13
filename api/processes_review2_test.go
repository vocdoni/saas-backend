package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestVotingProcessEmptyCensusRejected verifies the preflight rejects a process whose census has
// no voters (auth-only census, no members, no eligibility subsets) synchronously — the dry-run is
// invalid and publish returns 400 instead of a 202 that fails opaquely in the worker.
func TestVotingProcessEmptyCensusRejected(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, minimalVotingProcessRequest(orgAddress), processesCreateEndpoint)

	val := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", created.ProcessID, "check")
	c.Assert(val.Valid, qt.IsFalse)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
}

// minimalVotingProcessRequest builds a 1-question, member-less draft (empty census, no
// eligibility subset) — the smallest request that passes validation, used where the test does
// not need members.
func minimalVotingProcessRequest(orgAddress common.Address) *apicommon.CreateVotingProcessRequest {
	return &apicommon.CreateVotingProcessRequest{
		OrgAddress: orgAddress,
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
	data, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
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
		t, http.MethodPost, apiKey, minimalVotingProcessRequest(managed.Address), processesCreateEndpoint)
	c.Assert(proc.ProcessID, qt.Not(qt.Equals), "")

	// a key without voting:write is refused (403)
	noScopeBody := &apicommon.CreateAPIKeyRequest{Label: "noscope", Scopes: []string{ScopeManagedRead}}
	data, code = testRequest(t, http.MethodPost, token, noScopeBody, "organizations", orgAddr.String(), "apikeys")
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
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	// the dry-run reports it not ready (previously it passed the structural-only check)
	val := requestAndParse[apicommon.VotingProcessValidateResponse](t, http.MethodGet, token, nil, "processes", pid, "check")
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
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

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
		t, http.MethodPost, token, req, processesCreateEndpoint)

	// a draft has no on-chain results yet
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", created.ProcessID, "results")

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Error))

	// published: one results entry per question, each with its on-chain election id and status
	res := requestAndParse[apicommon.VotingProcessResultsResponse](
		t, http.MethodGet, "", nil, "processes", created.ProcessID, "results")
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
		t, http.MethodPost, token, req, processesCreateEndpoint)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Error))

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
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

	// a valid process + participant id resolves (public, 200)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, "", nil, "processes", created.ProcessID, "participant", ids[0])
	// a non-existent process is 404
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil,
		"processes", primitive.NewObjectID().Hex(), "participant", ids[0])
	// a malformed process id is 400
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "processes", "not-an-id", "participant", ids[0])
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
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
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
