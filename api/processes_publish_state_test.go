package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// TestPartiallyPublishedDraftIsImmutable pins that a draft some of whose questions already mined
// on-chain elections (a publish that failed halfway) can be neither updated nor deleted: both
// would erase the stored election ids, orphaning the mined elections and making the next publish
// mint duplicates. The caller is answered 409 (40904) and the mined id survives untouched.
func TestPartiallyPublishedDraftIsImmutable(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "partialpub123456")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	pid := created.ProcessID
	oid := objectID(c, pid)
	_, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 2)

	// simulate a publish that mined the first question's election and then failed
	upstream := internal.HexBytes(randomProcessID())
	c.Assert(testDB.SetQuestionPublished(questions[0].ID, upstream, "url", db.QuestionStatusReady), qt.IsNil)

	// a draft update is refused: it would replace the questions and erase the mined id
	edit := newVotingProcessRequest(orgAddress, ids)
	edit.Title = db.MultiLangString{"default": "edited after partial publish"}
	requestAndAssertError(errors.ErrProcessPartiallyPublished, t, http.MethodPut, token, edit, "processes", pid)

	// a delete is refused too: the mined election would be orphaned
	requestAndAssertError(errors.ErrProcessPartiallyPublished, t, http.MethodDelete, token, nil, "processes", pid)

	// the mined id survived both refusals, and the delete released its claim on the way out
	q, err := testDB.Question(questions[0].ID)
	c.Assert(err, qt.IsNil)
	c.Assert(q.UpstreamID.String(), qt.Equals, upstream.String())
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PublishInProgress(), qt.IsFalse)
}

// TestStatusChangeNoOpAndPerQuestionOutcomes pins the batch status-change semantics: a question
// already in the requested status is a successful no-op (the chain would reject the redundant
// transition, so a retry of a half-applied batch must not fail forever on it), a question in a
// terminal status asked for a different one rejects the whole batch with 400 before anything is
// submitted, and a mixed batch records each question's outcome individually in the job result
// instead of abandoning the rest at the first rejection.
func TestStatusChangeNoOpAndPerQuestionOutcomes(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "statusnoop123456")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint,
	)
	pid := created.ProcessID
	oid := objectID(c, pid)
	_, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 2)

	// mark both questions published directly; the upstream ids are fake, so any transition that
	// actually reaches the chain is rejected there — exactly what the mixed batch below needs.
	for i := range questions {
		c.Assert(testDB.SetQuestionPublished(
			questions[i].ID, internal.HexBytes(randomProcessID()), "url", db.QuestionStatusReady,
		), qt.IsNil)
	}

	// everything already READY: asking for READY is a completed job of no-ops, chain untouched
	job := enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.SetQuestionsStatusRequest{Status: "ready"}, "processes", pid, "questions", "status")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	for _, entry := range job.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(entry.NoOp, qt.IsTrue)
	}

	// a terminal question asked for a different status rejects the whole batch up front
	c.Assert(testDB.SetQuestionStatus(questions[0].ID, db.QuestionStatusEnded), qt.IsNil)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.SetQuestionsStatusRequest{Status: "paused"}, "processes", pid, "questions", "status")

	// mixed batch: ENDED→ENDED is a no-op, READY→ENDED is submitted (and rejected by the chain,
	// since the election id is fake) — the job reports each question's outcome individually.
	job = enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.SetQuestionsStatusRequest{Status: "ended"}, "processes", pid, "questions", "status")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	byID := map[string]db.QuestionJobResult{}
	for _, entry := range job.Result.Questions {
		byID[entry.QuestionID] = entry
	}
	ended := byID[questions[0].ID.Hex()]
	c.Assert(ended.Status, qt.Equals, db.JobStatusCompleted)
	c.Assert(ended.NoOp, qt.IsTrue)
	rejected := byID[questions[1].ID.Hex()]
	c.Assert(rejected.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(rejected.Error, qt.Not(qt.Equals), "")
}
