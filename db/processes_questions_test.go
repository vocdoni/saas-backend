package db

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// questionSet builds n questions for a process, titled so a rewrite is distinguishable.
func questionSet(org common.Address, n int, title string) []*VotingProcessQuestion {
	qs := make([]*VotingProcessQuestion, 0, n)
	for range n {
		qs = append(qs, &VotingProcessQuestion{
			OrgAddress: org,
			Type:       VotingTypeSingleChoice,
			Title:      MultiLangString{"default": title},
			Choices:    []Choice{{Title: MultiLangString{"default": "Yes"}, Value: 0}},
		})
	}
	return qs
}

// TestSetProcessQuestions covers the per-slot replacement that backs a draft update: ids are stable
// across a rewrite, a shorter set truncates the tail, and concurrent writers cannot strand a row
// (issue #614).
func TestSetProcessQuestions(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x61, 0x14}
	setupVotingProcessOrg(c, org)

	pid, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)

	ids, err := testDB.SetProcessQuestions(pid, questionSet(org, 3, "first"))
	c.Assert(err, qt.IsNil)
	c.Assert(ids, qt.HasLen, 3)

	// a rewrite of the same length keeps the ids: publish, the status syncer and eligibleMemberIds
	// all key off them, so they must survive an edit
	rewritten, err := testDB.SetProcessQuestions(pid, questionSet(org, 3, "second"))
	c.Assert(err, qt.IsNil)
	c.Assert(rewritten, qt.DeepEquals, ids)

	stored, err := testDB.QuestionsByProcess(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 3)
	for i := range stored {
		c.Assert(stored[i].Order, qt.Equals, i)
		c.Assert(stored[i].ProcessID, qt.Equals, pid)
		c.Assert(stored[i].Title["default"], qt.Equals, "second")
	}

	// a shorter set truncates the tail rather than leaving orphans behind
	shorter, err := testDB.SetProcessQuestions(pid, questionSet(org, 1, "third"))
	c.Assert(err, qt.IsNil)
	c.Assert(shorter, qt.HasLen, 1)
	c.Assert(shorter[0], qt.Equals, ids[0])
	stored, err = testDB.QuestionsByProcess(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 1)
}

// TestSetProcessQuestionsConcurrent is the storage-level regression for issue #614: overlapping
// writers must converge on exactly one question per slot, never leaving a stranded row for publish
// to turn into an extra on-chain election.
func TestSetProcessQuestionsConcurrent(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0x61, 0x14, 0x02}
	setupVotingProcessOrg(c, org)

	pid, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetProcessQuestions(pid, questionSet(org, 2, "initial"))
	c.Assert(err, qt.IsNil)

	const writers = 8
	results := make([][]primitive.ObjectID, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			results[i], errs[i] = testDB.SetProcessQuestions(pid, questionSet(org, 2, "concurrent"))
		})
	}
	wg.Wait()
	for i := range writers {
		c.Assert(errs[i], qt.IsNil)
		c.Assert(results[i], qt.HasLen, 2)
	}

	stored, err := testDB.QuestionsByProcess(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 2,
		qt.Commentf("concurrent writers must not strand a question: %d stored", len(stored)))
	c.Assert(stored[0].Order, qt.Equals, 0)
	c.Assert(stored[1].Order, qt.Equals, 1)
	c.Assert(stored[0].ID, qt.Not(qt.Equals), stored[1].ID)
}
