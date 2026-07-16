package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func setupVotingProcessOrg(c *qt.C, org common.Address) {
	err := testDB.SetOrganization(&Organization{Address: org, Active: true, CreatedAt: time.Now()})
	c.Assert(err, qt.IsNil)
}

func TestVotingProcessCRUD(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x11}
	setupVotingProcessOrg(c, org)

	vp := &VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}}
	id, err := testDB.SetVotingProcess(vp)
	c.Assert(err, qt.IsNil)
	c.Assert(id.IsZero(), qt.IsFalse)

	got, err := testDB.VotingProcess(id)
	c.Assert(err, qt.IsNil)
	c.Assert(got.OrgAddress, qt.Equals, org)
	c.Assert(got.Published, qt.IsFalse)

	// two questions in order
	q1 := &VotingProcessQuestion{ProcessID: id, OrgAddress: org, Order: 0, Type: VotingTypeSingleChoice}
	q2 := &VotingProcessQuestion{ProcessID: id, OrgAddress: org, Order: 1, Type: VotingTypeMultiChoice}
	q1ID, err := testDB.SetQuestion(q1)
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(q2)
	c.Assert(err, qt.IsNil)

	_, questions, err := testDB.ProcessWithQuestions(id)
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 2)
	c.Assert(questions[0].Order, qt.Equals, 0)
	c.Assert(questions[1].Order, qt.Equals, 1)

	// draft count
	n, err := testDB.CountVotingProcesses(org, DraftOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))

	// publish one question, reverse lookup, then reset
	upstream := internal.HexBytes("election-1")
	c.Assert(testDB.SetQuestionPublished(q1ID, upstream, "url", QuestionStatusReady), qt.IsNil)
	byUp, err := testDB.QuestionByUpstreamID(upstream)
	c.Assert(err, qt.IsNil)
	c.Assert(byUp.ID, qt.Equals, q1ID)
	c.Assert(byUp.Status, qt.Equals, QuestionStatusReady)

	// abandon keeps already-mined questions (a re-publish resumes the rest), so a mined
	// question's on-chain id survives ResetQuestionsPublish and the reverse lookup still resolves.
	c.Assert(testDB.ResetQuestionsPublish(id), qt.IsNil)
	byUp, err = testDB.QuestionByUpstreamID(upstream)
	c.Assert(err, qt.IsNil)
	c.Assert(byUp.ID, qt.Equals, q1ID)

	// publish flips draft count and published flag
	c.Assert(testDB.SetVotingProcessPublished(id), qt.IsNil)
	got, err = testDB.VotingProcess(id)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Published, qt.IsTrue)
	n, err = testDB.CountVotingProcesses(org, DraftOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
}

func TestClaimVotingProcessForPublish(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x12}
	setupVotingProcessOrg(c, org)
	id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)

	// first claim wins, second loses (until cleared)
	claimed, err := testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	claimed, err = testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsFalse)

	c.Assert(testDB.ClearVotingProcessPublishing(id), qt.IsNil)
	claimed, err = testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
}

// TestQuestionStatusSyncMethods covers the status-syncer DB methods: the syncable-candidate
// projection (only {READY,PAUSED,ENDED} with an upstreamId), the bulk status write (status +
// syncedAt), and the active-question count that backs the managed-org delete guard.
func TestQuestionStatusSyncMethods(t *testing.T) {
	c := qt.New(t)
	// unique org so the org-scoped count is unaffected by other tests sharing the database.
	org := common.Address{0x99, 0x5, 0x42}
	setupVotingProcessOrg(c, org)

	vpID, err := testDB.SetVotingProcess(&VotingProcess{
		OrgAddress: org, Published: true, Title: MultiLangString{"default": "S"},
	})
	c.Assert(err, qt.IsNil)

	// upstreamIds are prefixed to stay unique across the shared test database; the syncable-set
	// query is global (the syncer sweeps every org), so assertions check membership of these ids
	// rather than a total count.
	up := func(s string) internal.HexBytes { return internal.HexBytes("ssm-" + s) }
	seed := func(order int, upstream, status string) primitive.ObjectID {
		id, err := testDB.SetQuestion(&VotingProcessQuestion{
			ProcessID: vpID, OrgAddress: org, Order: order,
			UpstreamID: up(upstream), Status: status,
		})
		c.Assert(err, qt.IsNil)
		return id
	}
	ready := seed(0, "ready", QuestionStatusReady)
	paused := seed(1, "paused", QuestionStatusPaused)
	seed(2, "ended", QuestionStatusEnded)
	seed(3, "results", QuestionStatusResults)   // terminal → excluded from syncable
	seed(4, "canceled", QuestionStatusCanceled) // terminal → excluded from syncable
	// a draft (no upstreamId) is excluded from the syncable set
	_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: vpID, OrgAddress: org, Order: 5})
	c.Assert(err, qt.IsNil)

	// syncable candidates: our {ready, paused, ended} are present; terminal/draft ones are not.
	refs, err := testDB.QuestionsInSyncableStatus(context.Background())
	c.Assert(err, qt.IsNil)
	got := map[string]string{}
	for _, r := range refs {
		got[r.UpstreamID.String()] = r.Status
	}
	c.Assert(got[up("ready").String()], qt.Equals, QuestionStatusReady)
	c.Assert(got[up("paused").String()], qt.Equals, QuestionStatusPaused)
	c.Assert(got[up("ended").String()], qt.Equals, QuestionStatusEnded)
	_, hasResults := got[up("results").String()]
	c.Assert(hasResults, qt.IsFalse)
	_, hasCanceled := got[up("canceled").String()]
	c.Assert(hasCanceled, qt.IsFalse)

	// active (ready|paused) count backs the delete guard — org-scoped, so exactly 2 for our org
	n, err := testDB.CountActiveQuestions(org)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	// bulk reconcile: ready→ended, paused→results; stamps syncedAt
	c.Assert(testDB.SyncQuestionStatuses(context.Background(), []QuestionStatusChange{
		{UpstreamID: up("ready"), NewStatus: QuestionStatusEnded},
		{UpstreamID: up("paused"), NewStatus: QuestionStatusResults},
	}), qt.IsNil)

	gotReady, err := testDB.Question(ready)
	c.Assert(err, qt.IsNil)
	c.Assert(gotReady.Status, qt.Equals, QuestionStatusEnded)
	c.Assert(gotReady.SyncedAt.IsZero(), qt.IsFalse)
	gotPaused, err := testDB.Question(paused)
	c.Assert(err, qt.IsNil)
	c.Assert(gotPaused.Status, qt.Equals, QuestionStatusResults)

	// after the reconcile, no active questions remain
	n, err = testDB.CountActiveQuestions(org)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))

	// empty change set is a no-op
	c.Assert(testDB.SyncQuestionStatuses(context.Background(), nil), qt.IsNil)
}
