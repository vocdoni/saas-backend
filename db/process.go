package db

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CreateProcess creates a new process for an organization
func (ms *MongoStorage) SetProcess(process *Process) (primitive.ObjectID, error) {
	if process.OrgAddress.Cmp(common.Address{}) == 0 {
		return primitive.NilObjectID, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return primitive.NilObjectID, fmt.Errorf("failed to get organization %s: %w", process.OrgAddress, err)
	}
	// check that the census exists
	if !process.Census.ID.IsZero() {
		census, err := ms.Census(process.Census.ID.Hex())
		if err != nil {
			return primitive.NilObjectID, fmt.Errorf("failed to get census: %w", err)
		}
		if len(census.Published.Root) == 0 || len(census.Published.URI) == 0 {
			return primitive.NilObjectID, fmt.Errorf("census %s does not have a published root or URI", census.ID.Hex())
		}
	}

	if process.ID.IsZero() {
		// if the process doesn't exist, create its id
		process.ID = primitive.NewObjectID()
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.processes.InsertOne(ctx, process)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("failed to create process: %w", err)
	}

	return process.ID, nil
}

// DeleteProcess removes a process
func (ms *MongoStorage) DelProcess(processID primitive.ObjectID) error {
	if processID == primitive.NilObjectID {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the process from the database using the ID
	filter := bson.M{"_id": processID}
	_, err := ms.processes.DeleteOne(ctx, filter)
	return err
}

// Process retrieves a process from the DB based on its ID
func (ms *MongoStorage) Process(processID primitive.ObjectID) (*Process, error) {
	if processID == primitive.NilObjectID {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	process := &Process{}
	if err := ms.processes.FindOne(ctx, bson.M{"_id": processID}).Decode(process); err != nil {
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}

// Process retrieves a process from the DB based on its address
func (ms *MongoStorage) ProcessByAddress(address internal.HexBytes) (*Process, error) {
	if len(address) == 0 {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	process := &Process{}
	if err := ms.processes.FindOne(ctx, bson.M{"address": address}).Decode(process); err != nil {
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}
