package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// testCreateProvisionedOrganization creates an organization with eager on-chain account
// provisioning, which is required before it can publish elections.
func testCreateProvisionedOrganization(t *testing.T, token string) common.Address {
	t.Helper()
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://vproc-%d.com", internal.RandomInt(1000000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	return org.Address
}

// TestVotingProcessPublish exercises the full batch-publish path end-to-end against the
// in-process chain: it creates a 2-question process and publishes it (one on-chain election
// per question, submitted via the node batch endpoint), asserting both questions get an
// on-chain id and become ready and the process is marked published.
func TestVotingProcessPublish(t *testing.T) {
	c := qt.New(t)
	testStart := time.Now()
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// start the election immediately (no future start date)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	// publish -> 202 { jobId }, poll to completion
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
	c.Assert(job.Type, qt.Equals, db.JobTypePublishVotingProcess)

	// the process is now published with an on-chain election per question
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Published, qt.IsTrue)
	c.Assert(got.Questions, qt.HasLen, 2)
	for i := range got.Questions {
		c.Assert(len(got.Questions[i].UpstreamID) > 0, qt.IsTrue, qt.Commentf("question %d has no upstreamId", i))
	}

	// the request carried no start date ("start immediately"), so publish backfills the actual
	// on-chain start date and the read exposes it as a date around the publish moment
	c.Assert(got.StartDate, qt.Not(qt.Equals), "")
	startDate, err := time.Parse(time.RFC3339, got.StartDate)
	c.Assert(err, qt.IsNil)
	c.Assert(startDate.After(testStart.Add(-time.Minute)), qt.IsTrue,
		qt.Commentf("backfilled start date %s is before the test started", got.StartDate))
	c.Assert(startDate.Before(time.Now().Add(time.Minute)), qt.IsTrue,
		qt.Commentf("backfilled start date %s is in the future", got.StartDate))

	// the list endpoint carries the backfilled start date too
	list := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, token, nil, fmt.Sprintf("processes?orgAddress=%s&limit=100", orgAddress.Hex()),
	)
	listed := false
	for _, p := range list.Processes {
		if p.ID == pid {
			listed = true
			c.Assert(p.StartDate, qt.Equals, got.StartDate)
		}
	}
	c.Assert(listed, qt.IsTrue)

	// the single-question read reports the ready status
	q0 := requestAndParse[db.VotingProcessQuestion](
		t, http.MethodGet, "", nil, "processes", pid, "questions", got.Questions[0].ID.Hex())
	c.Assert(q0.Status, qt.Equals, db.QuestionStatusReady)

	// re-publishing a published process is an idempotent no-op (200, not a new job)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, "processes", pid, "publish")
}

// TestVotingProcessPublishRejectsStrayQuestion covers the last-line guard of issue #614: a database
// that already carries a duplicated question (written before the per-slot fix, or directly) must not
// have it turned into a real on-chain election, which is the one step nothing can undo. Publish is
// refused until the draft is saved again.
func TestVotingProcessPublishRejectsStrayQuestion(t *testing.T) {
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
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	// reproduce a corrupted state that is still representable now that the unique
	// (processId, order) index forbids same-slot duplicates: a stray tail row carrying this
	// processId that the process itself does not list (what a token-less sweep race leaves behind)
	stray := &db.VotingProcessQuestion{
		ProcessID: oid, OrgAddress: orgAddress, Order: 2,
		Title:     db.MultiLangString{"default": "stray"},
		Type:      db.VotingTypeSingleChoice,
		TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
		Choices:   []db.Choice{{Title: db.MultiLangString{"default": "Yes"}, Value: 0}},
	}
	_, err = testDB.SetQuestion(stray)
	c.Assert(err, qt.IsNil)

	stored, err := testDB.QuestionsByProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 3)

	// both the dry-run validation and publish itself refuse it
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(validation.Valid, qt.IsFalse)
	c.Assert(fmt.Sprint(validation.Errors), qt.Contains, "stored questions do not match the process")

	// server-side data state, not a bad request: 409 (40172), not 400
	requestAndAssertError(errors.ErrProcessQuestionsMismatch, t, http.MethodPost, token, nil, "processes", pid, "publish")

	// nothing reached the chain, and the process is still a draft
	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(after.Published, qt.IsFalse)
	for i := range after.Questions {
		c.Assert(after.Questions[i].UpstreamID, qt.HasLen, 0)
	}

	// saving the draft again reconciles the stored set, and publish is allowed once more
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, req, "processes", pid)
	stored, err = testDB.QuestionsByProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 2)
	validation = requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("errors: %v", validation.Errors))
}
