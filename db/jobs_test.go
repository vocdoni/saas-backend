package db

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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
	err = testDB.CompleteJob(jobID, CompleteJobResults{Added: added, Errors: errors})
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

		// the status the worker stored and the one derived from the entries must be the same, or a
		// job read across the gap between the counter and the closing write would flip its answer
		derivedStatus, derivedErrs := TerminalVoteBatchStatus(job.Result.Votes)
		c.Assert(derivedStatus, qt.Equals, job.Status)
		c.Assert(derivedErrs, qt.DeepEquals, job.Errors)
	})

	c.Run("unknown job", func(c *qt.C) {
		c.Assert(testDB.RecordBatchVoteOutcome("nope", 0, nil, ""), qt.Equals, ErrNotFound)
	})

	c.Run("index outside the batch", func(c *qt.C) {
		jobID := "batch-vote-job-bad-index"
		c.Assert(testDB.CreateVoteBatchJob(jobID, orgAddress, seed(2)), qt.IsNil)

		// an out-of-range write must not pad result.votes with nulls, must not advance the
		// progress counter, and must not be mistaken for a job that does not exist
		for _, index := range []int{2, 7, -1} {
			err := testDB.RecordBatchVoteOutcome(jobID, index, internal.HexBytes{0xff}, "")
			c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("index %d", index))
			c.Assert(err, qt.Not(qt.Equals), ErrNotFound, qt.Commentf("index %d", index))
		}

		job, err := testDB.Job(jobID)
		c.Assert(err, qt.IsNil)
		c.Assert(job.Result.Votes, qt.HasLen, 2)
		c.Assert(job.Added, qt.Equals, 0)
		c.Assert(job.Status, qt.Equals, JobStatusPending)
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
	c.Assert(job.ID, qt.Not(qt.Equals), bson.NilObjectID)

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

// TestFailInterruptedTxJobs pins the sweep for jobs orphaned by a stopped service process: the tx
// queues are in-memory, so a job still pending from a process that died has no worker left and
// would stay pending forever. The sweep fails the jobs whose owner stopped heartbeating (never
// replaying the transactions) and closes the pending entries of their batch vote jobs, while the
// jobs a live process keeps heartbeating, its own or a sibling's, are left alone — a replica
// starting next to live ones must not fail their work. A job of the sweeping process that it no
// longer heartbeats (nothing holds it) is swept too. Jobs written before jobs had an owner are
// judged by age; completed jobs and statusless import jobs are never touched.
func TestFailInterruptedTxJobs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.HexToAddress("0x1234567890123456789012345678901234567890")

	restore := TxJobStaleAfter
	TxJobStaleAfter = 200 * time.Millisecond
	c.Cleanup(func() { TxJobStaleAfter = restore })

	// two more handles on the same database, acting as replicas: one that dies, one that lives
	dead, err := New(mongoURI, testDB.database)
	c.Assert(err, qt.IsNil)
	c.Cleanup(dead.Close)
	live, err := New(mongoURI, testDB.database)
	c.Assert(err, qt.IsNil)
	c.Cleanup(live.Close)

	// the dead replica's pending tx job, completed job and half-reported batch vote job
	c.Assert(dead.CreateTxJob("interrupted-tx", JobTypePublishVotingProcess, org), qt.IsNil)
	c.Assert(dead.CreateTxJob("finished-tx", JobTypeSetProcessStatus, org), qt.IsNil)
	c.Assert(dead.SetJobStatus("finished-tx", JobStatusCompleted, nil, ""), qt.IsNil)
	votes := []VoteJobResult{
		{ProcessID: internal.HexBytes{1}, Status: JobStatusPending},
		{ProcessID: internal.HexBytes{2}, Status: JobStatusPending},
	}
	c.Assert(dead.CreateVoteBatchJob("interrupted-batch", org, votes), qt.IsNil)
	c.Assert(dead.RecordBatchVoteOutcome("interrupted-batch", 0, internal.HexBytes{0xaa}, ""), qt.IsNil)
	// a pending job written before jobs carried an owner, and an import job
	c.Assert(testDB.SetJob(&Job{
		JobID: "legacy-tx", Type: JobTypeRelayVote, OrgAddress: org, Errors: []string{},
		Status: JobStatusPending, CreatedAt: time.Now(),
	}), qt.IsNil)
	c.Assert(testDB.CreateJob("import-job", JobTypeOrgMembers, org, 10), qt.IsNil)
	// pending jobs of the live replica and of the sweeping process itself
	c.Assert(live.CreateTxJob("sibling-tx", JobTypeSetProcessStatus, org), qt.IsNil)
	c.Assert(testDB.CreateTxJob("own-tx", JobTypeSetProcessStatus, org), qt.IsNil)
	c.Assert(testDB.CreateTxJob("own-unheld-tx", JobTypeSetProcessStatus, org), qt.IsNil)

	// nothing is stale yet: a fresh job of a stopped process is not swept before its heartbeat
	// has had time to lapse
	n, err := live.FailInterruptedTxJobs()
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))

	// time passes: the live replica keeps heartbeating, the dead one does not
	time.Sleep(2 * TxJobStaleAfter)
	// a heartbeat only refreshes the given jobs of the calling process
	beats, err := live.HeartbeatTxJobs([]string{"sibling-tx", "own-tx"})
	c.Assert(err, qt.IsNil)
	c.Assert(beats, qt.Equals, int64(1))
	beats, err = testDB.HeartbeatTxJobs([]string{"own-tx"})
	c.Assert(err, qt.IsNil)
	c.Assert(beats, qt.Equals, int64(1))
	beats, err = testDB.HeartbeatTxJobs(nil)
	c.Assert(err, qt.IsNil)
	c.Assert(beats, qt.Equals, int64(0))

	n, err = testDB.FailInterruptedTxJobs()
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(4)) // interrupted-tx, interrupted-batch, legacy-tx, own-unheld-tx

	// the sweeper's own job that nothing held anymore is failed too
	job, err := testDB.Job("own-unheld-tx")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatusFailed)

	// the dead replica's pending tx job is failed with the restart explanation
	job, err = testDB.Job("interrupted-tx")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatusFailed)
	c.Assert(job.Error, qt.Contains, "restart")
	c.Assert(job.CompletedAt.IsZero(), qt.IsFalse)

	// so is the ownerless legacy job, by age
	job, err = testDB.Job("legacy-tx")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatusFailed)

	// the completed job is untouched
	job, err = testDB.Job("finished-tx")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatusCompleted)
	c.Assert(job.Error, qt.Equals, "")

	// the batch vote job is failed and only its still-pending entry was closed
	job, err = testDB.Job("interrupted-batch")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatusFailed)
	c.Assert(job.Result.Votes, qt.HasLen, 2)
	c.Assert(job.Result.Votes[0].Status, qt.Equals, JobStatusCompleted)
	c.Assert(job.Result.Votes[0].Error, qt.Equals, "")
	c.Assert(job.Result.Votes[1].Status, qt.Equals, JobStatusFailed)
	c.Assert(job.Result.Votes[1].Error, qt.Contains, "restart")

	// the live sibling's job and the sweeper's own job are still pending
	for _, id := range []string{"sibling-tx", "own-tx"} {
		job, err = testDB.Job(id)
		c.Assert(err, qt.IsNil)
		c.Assert(job.Status, qt.Equals, JobStatusPending, qt.Commentf("job %s", id))
		c.Assert(job.Error, qt.Equals, "")
	}

	// the import job carries no status field and is left alone
	job, err = testDB.Job("import-job")
	c.Assert(err, qt.IsNil)
	c.Assert(job.Status, qt.Equals, JobStatus(""))
	c.Assert(job.Error, qt.Equals, "")
	c.Assert(job.CompletedAt.IsZero(), qt.IsTrue)
}
