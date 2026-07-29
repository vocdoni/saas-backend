package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
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

	// publish flips draft count and published flag, and backfills the chain-resolved start date
	startDate := time.Now().Truncate(time.Millisecond)
	c.Assert(testDB.SetVotingProcessPublished(id, startDate), qt.IsNil)
	got, err = testDB.VotingProcess(id)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Published, qt.IsTrue)
	c.Assert(got.StartDate.Equal(startDate), qt.IsTrue)
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

// TestQuestionStatusSyncMethods covers the status-syncer DB methods: the org-scoped syncable-
// candidate projection (only {READY,PAUSED,ENDED} with an upstreamId, backing the delete guard) and
// the conditional reconcile write (status + syncedAt, applied only while the stored status still
// equals prev so a concurrent write is never clobbered).
func TestQuestionStatusSyncMethods(t *testing.T) {
	c := qt.New(t)
	// unique org so the org-scoped query is unaffected by other tests sharing the database.
	org := common.Address{0x99, 0x5, 0x42}
	setupVotingProcessOrg(c, org)

	vpID, err := testDB.SetVotingProcess(&VotingProcess{
		OrgAddress: org, Published: true, Title: MultiLangString{"default": "S"},
	})
	c.Assert(err, qt.IsNil)

	// upstreamIds are prefixed to stay unique across the shared test database.
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

	// org-scoped syncable candidates: exactly our {ready, paused, ended}; terminal/draft excluded.
	refs, err := testDB.SyncableQuestionsByOrg(org)
	c.Assert(err, qt.IsNil)
	c.Assert(refs, qt.HasLen, 3)
	got := map[string]string{}
	for _, r := range refs {
		got[r.UpstreamID.String()] = r.Status
	}
	c.Assert(got[up("ready").String()], qt.Equals, QuestionStatusReady)
	c.Assert(got[up("paused").String()], qt.Equals, QuestionStatusPaused)
	c.Assert(got[up("ended").String()], qt.Equals, QuestionStatusEnded)

	// conditional reconcile: ready→ended matches (stored == prev) and stamps syncedAt.
	matched, err := testDB.SetQuestionStatusSynced(up("ready"), QuestionStatusReady, QuestionStatusEnded)
	c.Assert(err, qt.IsNil)
	c.Assert(matched, qt.IsTrue)
	gotReady, err := testDB.Question(ready)
	c.Assert(err, qt.IsNil)
	c.Assert(gotReady.Status, qt.Equals, QuestionStatusEnded)
	c.Assert(gotReady.SyncedAt.IsZero(), qt.IsFalse)

	// a stale prev (no longer the stored value) does not match → the stored status is not clobbered.
	matched, err = testDB.SetQuestionStatusSynced(up("paused"), QuestionStatusReady, QuestionStatusCanceled)
	c.Assert(err, qt.IsNil)
	c.Assert(matched, qt.IsFalse)
	gotPaused, err := testDB.Question(paused)
	c.Assert(err, qt.IsNil)
	c.Assert(gotPaused.Status, qt.Equals, QuestionStatusPaused)

	// prev == next just refreshes syncedAt (the confirm-success stamp path).
	matched, err = testDB.SetQuestionStatusSynced(up("paused"), QuestionStatusPaused, QuestionStatusPaused)
	c.Assert(err, qt.IsNil)
	c.Assert(matched, qt.IsTrue)
	gotPaused, err = testDB.Question(paused)
	c.Assert(err, qt.IsNil)
	c.Assert(gotPaused.SyncedAt.IsZero(), qt.IsFalse)

	// unknown upstreamId → no match, no error; empty upstreamId → ErrInvalidData.
	matched, err = testDB.SetQuestionStatusSynced(up("nonexistent"), QuestionStatusReady, QuestionStatusEnded)
	c.Assert(err, qt.IsNil)
	c.Assert(matched, qt.IsFalse)
	_, err = testDB.SetQuestionStatusSynced(nil, QuestionStatusReady, QuestionStatusEnded)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestSetQuestionEligibleMemberIDs covers the targeted eligibility write: it leaves the fields the
// publish path owns alone, treats "no restriction" the same however it was stored, and refuses a
// write whose expected value is stale.
func TestSetQuestionEligibleMemberIDs(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x33}
	setupVotingProcessOrg(c, org)

	vpID, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)

	// a question stored with no restriction: EligibleMemberIDs is nil, i.e. null in the database.
	qID, err := testDB.SetQuestion(&VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: org, Type: VotingTypeSingleChoice,
		UpstreamID: internal.HexBytes{0xab}, Status: "READY",
	})
	c.Assert(err, qt.IsNil)

	// restricting it matches the stored null, and does not disturb the publish-owned fields
	c.Assert(testDB.SetQuestionEligibleMemberIDs(qID, nil, []string{"a", "b"}), qt.IsNil)
	got, err := testDB.Question(qID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.EligibleMemberIDs, qt.DeepEquals, []string{"a", "b"})
	c.Assert(got.UpstreamID, qt.DeepEquals, internal.HexBytes{0xab})
	c.Assert(got.Status, qt.Equals, "READY")

	// appending against the value we just read succeeds
	c.Assert(testDB.SetQuestionEligibleMemberIDs(qID, []string{"a", "b"}, []string{"a", "b", "c"}), qt.IsNil)

	// a stale expected value loses instead of clobbering the concurrent write
	err = testDB.SetQuestionEligibleMemberIDs(qID, []string{"a", "b"}, []string{"a", "b", "d"})
	c.Assert(err, qt.Equals, ErrStaleWrite)
	got, err = testDB.Question(qID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.EligibleMemberIDs, qt.DeepEquals, []string{"a", "b", "c"})

	// clearing it back to "everyone", then matching that empty list however it is stored
	c.Assert(testDB.SetQuestionEligibleMemberIDs(qID, []string{"a", "b", "c"}, nil), qt.IsNil)
	c.Assert(testDB.SetQuestionEligibleMemberIDs(qID, nil, []string{"e"}), qt.IsNil)

	// unknown question
	c.Assert(testDB.SetQuestionEligibleMemberIDs(primitive.NewObjectID(), nil, []string{"a"}), qt.Equals, ErrNotFound)
	c.Assert(testDB.SetQuestionEligibleMemberIDs(primitive.NilObjectID, nil, nil), qt.Equals, ErrInvalidData)
}

// TestSetVotingProcessKeepsPublishingMarker pins that editing a process does not release the claim
// a publish worker holds on it. SetVotingProcess replaces the whole document, so before the marker
// was a field on the struct any draft edit silently wiped it — and a second publish could then be
// claimed while the first was still running.
func TestSetVotingProcessKeepsPublishingMarker(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x13}
	setupVotingProcessOrg(c, org)
	id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)

	claimed, err := testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)

	vp, err := testDB.VotingProcess(id)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Publishing.IsZero(), qt.IsFalse, qt.Commentf("the claim must be readable through the struct"))
	c.Assert(vp.PublishInProgress(), qt.IsTrue)

	vp.Title = MultiLangString{"default": "edited"}
	_, err = testDB.SetVotingProcess(vp)
	c.Assert(err, qt.IsNil)

	got, err := testDB.VotingProcess(id)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Title["default"], qt.Equals, "edited")
	c.Assert(got.Publishing.IsZero(), qt.IsFalse)
	claimed, err = testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsFalse, qt.Commentf("an edit must not release the publish claim"))
}

// TestVotingProcessOmitsZeroPublishingMarker pins the omitempty contract the duplicate-publish
// guard rests on: it matches processes whose publishing field is ABSENT, and the stale sweep
// matches those whose field is old. A zero date written into the document would satisfy the second
// and make every draft ever created look like a crashed publish.
func TestVotingProcessOmitsZeroPublishingMarker(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x14}
	setupVotingProcessOrg(c, org)
	id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var doc bson.M
	c.Assert(testDB.votingProcesses.FindOne(ctx, bson.M{"_id": id}).Decode(&doc), qt.IsNil)
	_, present := doc["publishing"]
	c.Assert(present, qt.IsFalse, qt.Commentf("a never-claimed process must carry no publishing key"))

	stale, err := testDB.StaleVotingProcesses()
	c.Assert(err, qt.IsNil)
	c.Assert(stale, qt.Not(qt.Contains), id, qt.Commentf("a fresh draft is not a crashed publish"))

	claimed, err := testDB.ClaimVotingProcessForPublish(id)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
}

// TestPublishInProgress covers the predicate the mutating handlers gate on. The staleness boundary
// has to agree with ClaimVotingProcessForPublish's own cutoff, or a process could be unpublishable
// and uneditable at the same time — or claimable and editable at once.
func TestPublishInProgress(t *testing.T) {
	c := qt.New(t)
	now := time.Now()
	for name, tc := range map[string]struct {
		vp   VotingProcess
		want bool
	}{
		"never claimed":    {VotingProcess{}, false},
		"claimed just now": {VotingProcess{Publishing: now}, true},
		// the boundary itself is `<=`, matching the strict `$lt` the claim reclaims with, but a
		// marker exactly on the cutoff cannot be asserted: time.Since moves past it between
		// building this table and reading it. These bracket it instead.
		"claim still inside the window": {VotingProcess{Publishing: now.Add(-PublishStaleAfter + time.Minute)}, true},
		"claim just past the window":    {VotingProcess{Publishing: now.Add(-PublishStaleAfter - time.Minute)}, false},
		"claim long since stale":        {VotingProcess{Publishing: now.Add(-2 * PublishStaleAfter)}, false},
		"published, marker unset":       {VotingProcess{Published: true}, false},
		"published, marker lagged":      {VotingProcess{Published: true, Publishing: now}, false},
	} {
		c.Assert(tc.vp.PublishInProgress(), qt.Equals, tc.want, qt.Commentf("%s", name))
	}
}
