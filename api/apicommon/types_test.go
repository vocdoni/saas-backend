package apicommon

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// TestJobResponseFromDBBatchVoteTerminal covers the read-path derivation for batch vote jobs: the
// worker bumps the progress counter and writes the terminal status as two separate operations, so a
// job can legitimately be read with every envelope reported and no status stored — briefly during
// normal operation, or forever if the process died in between. The response must not call that
// pending.
func TestJobResponseFromDBBatchVoteTerminal(t *testing.T) {
	c := qt.New(t)

	batch := func(total, added int, votes []db.VoteJobResult) *db.Job {
		return &db.Job{
			JobID:  "job",
			Type:   db.JobTypeRelayVotes,
			Status: db.JobStatusPending,
			Total:  total,
			Added:  added,
			Result: &db.JobResult{Votes: votes},
		}
	}
	completed := db.VoteJobResult{Status: db.JobStatusCompleted}
	failed := db.VoteJobResult{Status: db.JobStatusFailed, Error: "vote already exists"}

	c.Run("every envelope succeeded", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 2, []db.VoteJobResult{completed, completed}))
		c.Assert(got.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(got.Errors, qt.HasLen, 0)
	})

	c.Run("one envelope failed", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 2, []db.VoteJobResult{completed, failed}))
		c.Assert(got.Status, qt.Equals, db.JobStatusFailed)
		c.Assert(got.Errors, qt.DeepEquals, []string{"vote 1: vote already exists"})
	})

	c.Run("still waiting on an envelope", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 1, []db.VoteJobResult{completed, {Status: db.JobStatusPending}}))
		c.Assert(got.Status, qt.Equals, db.JobStatusPending,
			qt.Commentf("a job with envelopes still outstanding is genuinely pending"))
		c.Assert(got.Result.Progress, qt.Equals, 50)
	})

	c.Run("a stored terminal status wins", func(c *qt.C) {
		job := batch(2, 2, []db.VoteJobResult{completed, failed})
		job.Status = db.JobStatusCompleted // already closed by the worker
		got := JobResponseFromDB(job)
		c.Assert(got.Status, qt.Equals, db.JobStatusCompleted,
			qt.Commentf("the derivation only fills a gap; it never overrides what the worker wrote"))
	})

	c.Run("other job types are untouched", func(c *qt.C) {
		got := JobResponseFromDB(&db.Job{
			JobID: "job", Type: db.JobTypeOrgMembers, Status: db.JobStatusPending, Total: 2, Added: 2,
		})
		c.Assert(got.Status, qt.Equals, db.JobStatusPending,
			qt.Commentf("an import job's added==total does not mean it is finished"))
	})
}
