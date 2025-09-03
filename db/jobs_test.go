package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestJobOperations(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

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
	c.Assert(len(job.Errors), qt.Equals, 0)
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

func TestSetJob(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

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
	c.Assert(len(retrievedJob.Errors), qt.Equals, 2)
}
