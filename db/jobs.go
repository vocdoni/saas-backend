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
	"go.vocdoni.io/dvote/log"
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

// Jobs retrieves paginated jobs for an organization from the database.
func (ms *MongoStorage) Jobs(orgAddress common.Address, page, pageSize int, jobType *JobType) (int, []Job, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create filter
	filter := bson.M{
		"orgAddress": orgAddress,
	}

	// Add job type filter if specified
	if jobType != nil {
		filter["type"] = *jobType
	}

	// Count total documents
	totalCount, err := ms.jobs.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to count jobs: %w", err)
	}

	// Calculate total pages
	totalPages := int((totalCount + int64(pageSize) - 1) / int64(pageSize))

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Set up options for pagination - sort by creation date descending (newest first)
	findOptions := options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	// Execute the find operation with pagination
	cursor, err := ms.jobs.Find(ctx, filter, findOptions)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get jobs: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Decode results
	var jobs []Job
	if err = cursor.All(ctx, &jobs); err != nil {
		return 0, nil, fmt.Errorf("failed to decode jobs: %w", err)
	}

	return totalPages, jobs, nil
}
