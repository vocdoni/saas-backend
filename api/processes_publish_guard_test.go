package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// claimedDraft is a process sitting in the publish window, with what a test needs to poke at it.
type claimedDraft struct {
	pid string
	oid primitive.ObjectID
	qid string // the second (subset-restricted) question
	ids []string
}

// newClaimedDraft creates a draft and claims it for publishing the way the publish handler does,
// without running the worker: the process is then in the state the guard exists for — unpublished,
// questions still carrying no UpstreamID, and a live worker (as far as anything stored can tell)
// building elections from the snapshot it took.
func newClaimedDraft(t *testing.T, token string, orgAddress common.Address) claimedDraft {
	t.Helper()
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	got := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", created.ProcessID)
	oid, err := primitive.ObjectIDFromHex(created.ProcessID)
	qt.Assert(t, err, qt.IsNil)
	claimed, err := testDB.ClaimVotingProcessForPublish(oid)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, claimed, qt.IsTrue)
	return claimedDraft{pid: created.ProcessID, oid: oid, qid: got.Questions[1].ID.Hex(), ids: ids}
}

// TestVotingProcessMutationsRefusedWhilePublishing covers the window between a publish being
// claimed and the elections being mined. Nothing in the stored state says "draft" any less during
// it — published is false and no question has an UpstreamID yet — so every mutating handler used to
// take its draft path and edit the process behind the worker's back.
func TestVotingProcessMutationsRefusedWhilePublishing(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	draft := newClaimedDraft(t, token, orgAddress)
	pid, oid, qid, ids := draft.pid, draft.oid, draft.qid, draft.ids

	// rewriting the draft would replace the document and recreate the questions with new ids
	upd := newVotingProcessRequest(orgAddress, ids)
	upd.Title = db.MultiLangString{"default": "Updated title"}
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodPut, token, upd, "processes", pid)

	// changing eligibility would store a list the election being minted knows nothing about
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids},
		"processes", pid, "questions", qid, "census")

	// deleting would leave whatever the worker already mined on chain with no process at all
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodDelete, token, nil, "processes", pid)

	// none of the refusals changed anything
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Title["default"], qt.Equals, "Test process")
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.DeepEquals, ids[:1])

	// and the guard is a gate, not a wall: releasing the claim makes the process editable again
	c.Assert(testDB.ClearVotingProcessPublishing(oid), qt.IsNil)
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, upd, "processes", pid)
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Title["default"], qt.Equals, "Updated title")
}

// TestVotingProcessMutationsAllowedAfterStalePublishMarker covers the other side of the rule: a
// marker left by a crashed worker must not lock the process out of editing, since it does not lock
// it out of publishing either. Editing then has to clear it — the draft rewrite replaces the whole
// document, so writing a stale marker back would leave the process reported as stale forever.
func TestVotingProcessMutationsAllowedAfterStalePublishMarker(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	draft := newClaimedDraft(t, token, orgAddress)
	pid, oid, qid, ids := draft.pid, draft.oid, draft.qid, draft.ids

	restore := db.PublishStaleAfter
	db.PublishStaleAfter = -time.Minute
	defer func() { db.PublishStaleAfter = restore }()

	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids},
		"processes", pid, "questions", qid, "census")

	upd := newVotingProcessRequest(orgAddress, ids)
	upd.Title = db.MultiLangString{"default": "Updated title"}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, upd, "processes", pid)

	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Publishing.IsZero(), qt.IsTrue,
		qt.Commentf("editing a draft must release a stale marker, not write it back"))
	stale, err := testDB.StaleVotingProcesses()
	c.Assert(err, qt.IsNil)
	c.Assert(stale, qt.Not(qt.Contains), oid)
}
