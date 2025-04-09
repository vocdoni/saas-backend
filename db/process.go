package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetProcess creates a new process or updates an existing one for an organization.
// If the process already exists and is in draft mode, it will be updated.
func (ms *MongoStorage) SetProcess(process *Process) error {
	if len(process.ID) == 0 || len(process.OrgAddress) == 0 || len(process.PublishedCensus.Root) == 0 {
		return ErrInvalidData
	}

	// check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	// check that the publishedCensus and if not create it
	if _, err := ms.PublishedCensus(process.PublishedCensus.Root, process.PublishedCensus.URI,
		process.PublishedCensus.Census.ID.Hex()); err != nil {
		if err != ErrNotFound {
			return fmt.Errorf("failed to get publishedCensus: %w", err)
		}
		if err := ms.SetPublishedCensus(&process.PublishedCensus); err != nil {
			return fmt.Errorf("failed to create publishedCensus: %w", err)
		}
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use ReplaceOne with upsert option to either update an existing process or insert a new one
	filter := bson.M{"_id": process.ID}
	opts := options.Replace().SetUpsert(true)
	if _, err := ms.processes.ReplaceOne(ctx, filter, process, opts); err != nil {
		return fmt.Errorf("failed to create or update process: %w", err)
	}

	return nil
}

// DeleteProcess removes a process and all its participants
func (ms *MongoStorage) DelProcess(processID []byte) error {
	if len(processID) == 0 {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the process from the database using the ID
	filter := bson.M{"_id": processID}
	_, err := ms.processes.DeleteOne(ctx, filter)
	return err
}

// Process retrieves a process from the DB based on it ID
func (ms *MongoStorage) Process(processID []byte) (*Process, error) {
	if len(processID) == 0 {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	process := &Process{}
	if err := ms.processes.FindOne(ctx, bson.M{"_id": processID}).Decode(process); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}
