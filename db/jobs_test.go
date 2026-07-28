package db

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestJobOperations(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	// Test data
	jobID := "test-job-123"
	jobType := JobTypeOrgMembers
	orgAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	total := 100

	// Test CreateJob
	err := testDB.CreateJob(jobID, jobType, orgAddress, total)
	c.Assert(err, qt.IsNil)

	// Test Job retrieval
	job, err := testDB.Job(jobID)
	c.Assert(err, qt.IsNil)
	c.Assert(job, qt.IsNotNil)
	c.Assert(job.JobID, qt.Equals, jobID)
	c.Assert(job.Type, qt.Equals, jobType)
	c.Assert(job.OrgAddress, qt.Equals, orgAddress)
	c.Assert(job.Total, qt.Equals, total)
	c.Assert(job.Added, qt.Equals, 0)
	c.Assert(job.Errors, qt.HasLen, 0)
	c.Assert(job.CreatedAt.IsZero(), qt.IsFalse)
	c.Assert(job.CompletedAt.IsZero(), qt.IsTrue)

	// Test CompleteJob
	added := 85
	errors := []string{"error 1", "error 2"}
	err = testDB.CompleteJob(jobID, added, errors)
	c.Assert(err, qt.IsNil)

	// Test Job retrieval after completion
	job, err = testDB.Job(jobID)
	c.Assert(err, qt.IsNil)
	c.Assert(job.Added, qt.Equals, added)
	c.Assert(job.Errors, qt.DeepEquals, errors)
	c.Assert(job.CompletedAt.IsZero(), qt.IsFalse)

	// Test non-existent job
	_, err = testDB.Job("non-existent-job")
	c.Assert(err, qt.Equals, ErrNotFound)
}

// TestRecordBatchVoteOutcome checks the bookkeeping of a batch vote relay: the workers
// report their envelopes concurrently and in no particular order, each entry keeps the
// process id and nullifier it was seeded with, and the job is closed exactly once, by
// whichever worker reports last.
func TestRecordBatchVoteOutcome(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	seed := func(n int) []VoteJobResult {
		votes := make([]VoteJobResult, n)
		for i := range votes {
			votes[i] = VoteJobResult{
				ProcessID: internal.HexBytes{byte(i)},
				Nullifier: internal.HexBytes{0xaa, byte(i)},
				Status:    JobStatusPending,
			}
		}
		return votes
	}

	c.Run("all votes accepted", func(c *qt.C) {
		jobID := "batch-vote-job-ok"
		c.Assert(testDB.CreateVoteBatchJob(jobID, orgAddress, seed(4)), qt.IsNil)

		var wg sync.WaitGroup
		for i := range 4 {
			wg.Go(func() {
				c.Check(testDB.RecordBatchVoteOutcome(jobID, i, internal.HexBytes{0xaa, byte(i)}, ""), qt.IsNil)
			})
		}
		wg.Wait()

		job, err := testDB.Job(jobID)
		c.Assert(err, qt.IsNil)
		c.Assert(job.Status, qt.Equals, JobStatusCompleted)
		c.Assert(job.Added, qt.Equals, 4)
		c.Assert(job.Errors, qt.HasLen, 0)
		c.Assert(job.CompletedAt.IsZero(), qt.IsFalse)
		c.Assert(job.Result.Votes, qt.HasLen, 4)
		for i, vote := range job.Result.Votes {
			c.Assert(vote.Status, qt.Equals, JobStatusCompleted)
			c.Assert(vote.ProcessID, qt.DeepEquals, internal.HexBytes{byte(i)})
			c.Assert(vote.VoteID, qt.DeepEquals, internal.HexBytes{0xaa, byte(i)})
			c.Assert(vote.Error, qt.Equals, "")
		}
	})

	c.Run("one vote rejected", func(c *qt.C) {
		jobID := "batch-vote-job-partial"
		c.Assert(testDB.CreateVoteBatchJob(jobID, orgAddress, seed(3)), qt.IsNil)

		c.Assert(testDB.RecordBatchVoteOutcome(jobID, 0, internal.HexBytes{0xaa, 0}, ""), qt.IsNil)
		c.Assert(testDB.RecordBatchVoteOutcome(jobID, 1, nil, "vote already exists"), qt.IsNil)

		// still open: the last envelope has not reported yet
		job, err := testDB.Job(jobID)
		c.Assert(err, qt.IsNil)
		c.Assert(job.Status, qt.Equals, JobStatusPending)

		c.Assert(testDB.RecordBatchVoteOutcome(jobID, 2, internal.HexBytes{0xaa, 2}, ""), qt.IsNil)

		job, err = testDB.Job(jobID)
		c.Assert(err, qt.IsNil)
		c.Assert(job.Status, qt.Equals, JobStatusFailed)
		c.Assert(job.Added, qt.Equals, 3)
		c.Assert(job.Errors, qt.DeepEquals, []string{"vote 1: vote already exists"})
		// the votes that did land keep their outcome, and the one that failed keeps the
		// nullifier it was seeded with, so the caller can tell which vote to retry
		c.Assert(job.Result.Votes[0].Status, qt.Equals, JobStatusCompleted)
		c.Assert(job.Result.Votes[1].Status, qt.Equals, JobStatusFailed)
		c.Assert(job.Result.Votes[1].Error, qt.Equals, "vote already exists")
		c.Assert(job.Result.Votes[1].Nullifier, qt.DeepEquals, internal.HexBytes{0xaa, 1})
		c.Assert(job.Result.Votes[1].VoteID, qt.HasLen, 0)
		c.Assert(job.Result.Votes[2].Status, qt.Equals, JobStatusCompleted)
	})

	c.Run("unknown job", func(c *qt.C) {
		c.Assert(testDB.RecordBatchVoteOutcome("nope", 0, nil, ""), qt.Equals, ErrNotFound)
	})
}

func TestSetJob(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	// Test data
	job := &Job{
		JobID:       "test-job-456",
		Type:        JobTypeCensusParticipants,
		OrgAddress:  common.HexToAddress("0x9876543210987654321098765432109876543210"),
		Total:       50,
		Added:       25,
		Errors:      []string{"test error"},
		CreatedAt:   time.Now(),
		CompletedAt: time.Now(),
	}

	// Test SetJob (create)
	err := testDB.SetJob(job)
	c.Assert(err, qt.IsNil)
	c.Assert(job.ID, qt.Not(qt.Equals), primitive.NilObjectID)

	// Test SetJob (update)
	job.Added = 30
	job.Errors = append(job.Errors, "another error")
	err = testDB.SetJob(job)
	c.Assert(err, qt.IsNil)

	// Verify update
	retrievedJob, err := testDB.Job(job.JobID)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedJob.Added, qt.Equals, 30)
	c.Assert(retrievedJob.Errors, qt.HasLen, 2)
}
