package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetJob creates a new job record in the database or updates an existing one.
func (ms *MongoStorage) SetJob(job *Job) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// If no ID is set, create a new one
	if job.ID == primitive.NilObjectID {
		job.ID = primitive.NewObjectID()
	}

	// Create update document
	updateDoc, err := dynamicUpdateDocument(job, nil)
	if err != nil {
		return fmt.Errorf("failed to create update document: %w", err)
	}

	// Upsert the job
	filter := bson.M{"jobId": job.JobID}
	opts := options.Update().SetUpsert(true)
	_, err = ms.jobs.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return fmt.Errorf("failed to upsert job: %w", err)
	}

	return nil
}

// Job retrieves a job by its jobId.
func (ms *MongoStorage) Job(jobID string) (*Job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var job Job
	filter := bson.M{"jobId": jobID}
	err := ms.jobs.FindOne(ctx, filter).Decode(&job)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get job: %w", err)
	}

	return &job, nil
}

// CreateJob creates a new job record with initial data.
func (ms *MongoStorage) CreateJob(jobID string, jobType JobType, orgAddress common.Address, total int) error {
	job := &Job{
		JobID:      jobID,
		Type:       jobType,
		OrgAddress: orgAddress,
		Total:      total,
		Added:      0,
		Errors:     []string{},
		CreatedAt:  time.Now(),
	}

	return ms.SetJob(job)
}

// CreateTxJob inserts a new transaction job in the pending state. Unlike CreateJob
// (import jobs), it carries no Total/Added counters; the outcome is recorded later
// via SetJobStatus.
func (ms *MongoStorage) CreateTxJob(jobID string, jobType JobType, orgAddress common.Address) error {
	return ms.SetJob(&Job{
		JobID:      jobID,
		Type:       jobType,
		OrgAddress: orgAddress,
		Errors:     []string{},
		Status:     JobStatusPending,
		CreatedAt:  time.Now(),
	})
}

// SetJobStatus records the terminal outcome of a transaction job. On success pass
// the result and an empty errMsg; on failure pass a nil result and the error message.
func (ms *MongoStorage) SetJobStatus(jobID string, status JobStatus, result *JobResult, errMsg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	set := bson.M{
		"status":      status,
		"completedAt": time.Now(),
	}
	if result != nil {
		set["result"] = result
	}
	if errMsg != "" {
		set["error"] = errMsg
	}

	_, err := ms.jobs.UpdateOne(ctx, bson.M{"jobId": jobID}, bson.M{"$set": set})
	if err != nil {
		return fmt.Errorf("failed to set job status: %w", err)
	}
	return nil
}

// CompleteJob updates a job with final results when it completes.
func (ms *MongoStorage) CompleteJob(jobID string, added int, errors []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	filter := bson.M{"jobId": jobID}
	update := bson.M{
		"$set": bson.M{
			"added":       added,
			"errors":      errors,
			"completedAt": time.Now(),
		},
	}

	_, err := ms.jobs.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to complete job: %w", err)
	}

	return nil
}

// UpdateJobProgress records the running count of processed items of an import job WITHOUT marking it
// completed (no completedAt), so a client polling GET /jobs sees live progress. CompleteJob stamps the
// final count + errors + completedAt at the end.
func (ms *MongoStorage) UpdateJobProgress(jobID string, added int) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	_, err := ms.jobs.UpdateOne(ctx, bson.M{"jobId": jobID}, bson.M{"$set": bson.M{"added": added}})
	if err != nil {
		return fmt.Errorf("failed to update job progress: %w", err)
	}
	return nil
}

// Jobs retrieves paginated jobs for an organization from the database.
func (ms *MongoStorage) Jobs(orgAddress common.Address, page, limit int64, jobType *JobType) (int64, []Job, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}

	// Create filter
	filter := bson.M{
		"orgAddress": orgAddress,
	}

	// Add job type filter if specified
	if jobType != nil {
		filter["type"] = *jobType
	}

	// sort by creation date descending (newest first)
	findOptions := options.Find().
		SetSort(bson.D{
			{Key: "createdAt", Value: -1},
		})

	return paginatedDocuments[Job](ms.jobs, page, limit, filter, findOptions)
}

// DeleteJobsByOrg removes every job (import and transaction) owned by the given
// organization. Best-effort cleanup used when tearing down an organization.
// Returns the number of deleted job documents.
func (ms *MongoStorage) DeleteJobsByOrg(orgAddress common.Address) (int64, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.jobs.DeleteMany(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return 0, fmt.Errorf("failed to delete jobs by org: %w", err)
	}
	return res.DeletedCount, nil
}
