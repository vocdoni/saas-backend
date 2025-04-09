package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// SetProcess creates a new process for an organization
func (ms *MongoStorage) SetProcess(process *Process) error {
	if len(process.ID) == 0 || len(process.OrgAddress) == 0 || len(process.PublishedCensus.Root) == 0 {
		return ErrInvalidData
	}

	// Check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}

	// Check that the publishedCensus exists and if not create it
	if _, err := ms.PublishedCensus(process.PublishedCensus.Root, process.PublishedCensus.URI,
		process.PublishedCensus.Census.ID.Hex()); err != nil {
		if err != ErrNotFound {
			return fmt.Errorf("failed to get publishedCensus: %w", err)
		}
		if err := ms.SetPublishedCensus(&process.PublishedCensus); err != nil {
			return fmt.Errorf("failed to create publishedCensus: %w", err)
		}
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		if _, err := ms.processes.InsertOne(sessCtx, process); err != nil {
			return fmt.Errorf("failed to create process: %w", err)
		}
		return nil
	})
}

// DelProcess removes a process and all its participants
func (ms *MongoStorage) DelProcess(processID []byte) error {
	if len(processID) == 0 {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the process from the database using the ID
		filter := bson.M{"_id": processID}
		_, err := ms.processes.DeleteOne(sessCtx, filter)
		return err
	})
}

// Process retrieves a process from the DB based on it ID
func (ms *MongoStorage) Process(processID []byte) (*Process, error) {
	if len(processID) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var process *Process
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		process = &Process{}
		if err := ms.processes.FindOne(sessCtx, bson.M{"_id": processID}).Decode(process); err != nil {
			return fmt.Errorf("failed to get process: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return process, nil
}
