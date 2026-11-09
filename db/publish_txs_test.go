package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// newPublishTxProcess stores a draft of org with two questions and returns it with them.
func newPublishTxProcess(c *qt.C, org common.Address) (*VotingProcess, []bson.ObjectID) {
	setupVotingProcessOrg(c, org)
	vp := &VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}}
	id, err := testDB.SetVotingProcess(vp)
	c.Assert(err, qt.IsNil)
	ids, err := testDB.SetProcessQuestions(id, questionSet(org, 2, "q"))
	c.Assert(err, qt.IsNil)
	return vp, ids
}

// publishTx builds a record with a one-byte hash.
func publishTx(hash byte, nonce uint32) PublishTx {
	return PublishTx{
		Hash: internal.HexBytes{hash}, Nonce: nonce, MetadataURL: "https://meta/q.json",
		SubmittedAt: time.Now(),
	}
}

// TestQuestionPublishTxs covers the record lifecycle: appends only at the same nonce, the
// predicted id stored on the matching record, a dropped record removed, and the records unset
// only at the nonce they hold.
func TestQuestionPublishTxs(t *testing.T) {
	c := qt.New(t)
	vp, ids := newPublishTxProcess(c, common.Address{0x31})
	q := ids[0]

	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, q, publishTx(0x01, 7)), qt.IsNil)
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, q, publishTx(0x02, 7)), qt.IsNil)
	// the same-nonce rule: a record at another nonce, or under another process, matches nothing
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, q, publishTx(0x03, 8)), qt.Equals, ErrConflict)
	c.Assert(testDB.AppendQuestionPublishTx(bson.NewObjectID(), q, publishTx(0x03, 7)), qt.Equals, ErrConflict)

	c.Assert(testDB.SetQuestionPublishTxUpstreamID(q, internal.HexBytes{0x02}, internal.HexBytes{0xee}), qt.IsNil)
	got, err := testDB.Question(q)
	c.Assert(err, qt.IsNil)
	c.Assert(got.PublishTxs, qt.HasLen, 2)
	c.Assert(got.PublishTxs[0].UpstreamID, qt.HasLen, 0)
	c.Assert([]byte(got.PublishTxs[1].UpstreamID), qt.DeepEquals, []byte{0xee})

	c.Assert(testDB.RemoveQuestionPublishTx(q, internal.HexBytes{0x02}), qt.IsNil)
	got, err = testDB.Question(q)
	c.Assert(err, qt.IsNil)
	c.Assert(got.PublishTxs, qt.HasLen, 1)

	// dead at another nonce: left alone; at its nonce: unset
	c.Assert(testDB.ClearQuestionPublishTxs(q, 8), qt.IsNil)
	got, err = testDB.Question(q)
	c.Assert(err, qt.IsNil)
	c.Assert(got.PublishTxs, qt.HasLen, 1)
	c.Assert(testDB.ClearQuestionPublishTxs(q, 7), qt.IsNil)
	got, err = testDB.Question(q)
	c.Assert(err, qt.IsNil)
	c.Assert(got.PublishTxs, qt.HasLen, 0)

	// once the last record was pulled (an empty array) or unset, any nonce may be recorded again
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, q, publishTx(0x04, 9)), qt.IsNil)
	c.Assert(testDB.RemoveQuestionPublishTx(q, internal.HexBytes{0x04}), qt.IsNil)
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, q, publishTx(0x05, 10)), qt.IsNil)

	// recording the election drops the records
	c.Assert(testDB.SetQuestionPublished(q, internal.HexBytes{0xaa}, "https://meta/q.json", QuestionStatusReady), qt.IsNil)
	got, err = testDB.Question(q)
	c.Assert(err, qt.IsNil)
	c.Assert(got.PublishTxs, qt.HasLen, 0)
}

// TestVotingProcessPublishSubmitted covers the flag: set only under the claim, refusing a draft
// write while set, and cleared only once no question without an election holds a record.
func TestVotingProcessPublishSubmitted(t *testing.T) {
	c := qt.New(t)
	vp, ids := newPublishTxProcess(c, common.Address{0x32})

	c.Assert(testDB.SetVotingProcessPublishSubmitted(vp.ID, "not-the-owner"), qt.Equals, ErrConflict)
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(vp.ID, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testDB.SetVotingProcessPublishSubmitted(vp.ID, owner), qt.IsNil)
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, ids[0], publishTx(0x01, 3)), qt.IsNil)
	c.Assert(testDB.ClearVotingProcessPublishing(vp.ID, owner), qt.IsNil)

	read, err := testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(read.PublishSubmitted, qt.IsTrue)
	// a draft edit, with or without a revision token, is refused while it is set
	read.PublishSubmitted = false
	c.Assert(testDB.SetVotingProcessDraft(read, time.Time{}), qt.Equals, ErrConflict)
	c.Assert(testDB.SetVotingProcessDraft(read, read.UpdatedAt), qt.Equals, ErrConflict)

	cleared, err := testDB.ClearVotingProcessPublishSubmitted(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(cleared, qt.IsFalse)
	c.Assert(testDB.ClearQuestionPublishTxs(ids[0], 3), qt.IsNil)
	cleared, err = testDB.ClearVotingProcessPublishSubmitted(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(cleared, qt.IsTrue)
	read, err = testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(read.PublishSubmitted, qt.IsFalse)
	c.Assert(testDB.SetVotingProcessDraft(read, time.Time{}), qt.IsNil)

	// publishing the process clears the flag with the claim
	owner, claimed, err = testDB.ClaimVotingProcessForPublish(vp.ID, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testDB.SetVotingProcessPublishSubmitted(vp.ID, owner), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPublished(vp.ID, owner, time.Time{}), qt.IsNil)
	read, err = testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(read.PublishSubmitted, qt.IsFalse)
}

// TestResetQuestionsPublishKeepsPublishTxs (AC5): a reset leaves a question that holds records
// untouched, and still resets the others.
func TestResetQuestionsPublishKeepsPublishTxs(t *testing.T) {
	c := qt.New(t)
	vp, ids := newPublishTxProcess(c, common.Address{0x33})
	for _, id := range ids {
		c.Assert(testDB.SetQuestionStatus(id, QuestionStatusReady), qt.IsNil)
	}
	c.Assert(testDB.AppendQuestionPublishTx(vp.ID, ids[0], publishTx(0x01, 4)), qt.IsNil)

	c.Assert(testDB.ResetQuestionsPublish(vp.ID), qt.IsNil)

	held, err := testDB.Question(ids[0])
	c.Assert(err, qt.IsNil)
	c.Assert(held.Status, qt.Equals, QuestionStatusReady)
	c.Assert(held.PublishTxs, qt.HasLen, 1)
	reset, err := testDB.Question(ids[1])
	c.Assert(err, qt.IsNil)
	c.Assert(reset.Status, qt.Equals, "")
}
