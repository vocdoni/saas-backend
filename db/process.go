package db

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
)

// CreateProcess creates a new process for an organization
func (ms *MongoStorage) SetProcess(process *Process) error {
	if len(process.ID) == 0 || process.OrgAddress.Cmp(common.Address{}) == 0 || len(process.Census.ID) == 0 {
		return ErrInvalidData
	}

	// check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return fmt.Errorf("failed to get organization %s: %w", process.OrgAddress, err)
	}
	// check that the census exists
	census, err := ms.Census(process.Census.ID)
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	if len(census.Published.Root) == 0 || len(census.Published.URI) == 0 {
		return fmt.Errorf("census %s does not have a published root or URI", census.ID)
	}

	// TODO create the census if not found?

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processes.InsertOne(ctx, process); err != nil {
		return fmt.Errorf("failed to create process: %w", err)
	}

	return nil
}

// DeleteProcess removes a process
func (ms *MongoStorage) DelProcess(processID internal.HexBytes) error {
	if len(processID) == 0 {
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
func (ms *MongoStorage) Process(processID internal.HexBytes) (*Process, error) {
	if len(processID) == 0 {
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
