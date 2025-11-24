package db

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetProcess creates a new process or updates an existing one for an organization.
// If the process already exists and is in draft mode, it will be updated.
func (ms *MongoStorage) SetProcess(process *Process) (internal.ObjectID, error) {
	// validate input
	if (process.OrgAddress.Cmp(common.Address{}) == 0) {
		return internal.NilObjectID, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to get organization %s: %w", process.OrgAddress, err)
	}
	// check that the census exists
	if !process.Census.ID.IsZero() {
		census, err := ms.Census(process.Census.ID)
		if err != nil {
			return internal.NilObjectID, fmt.Errorf("failed to get census: %w", err)
		}
		if len(census.Published.Root) == 0 || len(census.Published.URI) == 0 {
			return internal.NilObjectID, fmt.Errorf("census %s does not have a published root or URI", census.ID.String())
		}
		if census.OrgAddress.Cmp(process.OrgAddress) != 0 {
			return internal.NilObjectID, fmt.Errorf("census %s does not belong to organization %s",
				census.ID.String(), process.OrgAddress.String())
		}
	}

	if process.ID.IsZero() {
		// if the process doesn't exist, create its id
		process.ID = internal.NewObjectID()
	}

	updateDoc, err := dynamicUpdateDocument(process, nil)
	if err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to create update document: %w", err)
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// Use ReplaceOne with upsert option to either update an existing process or insert a new one
	filter := bson.M{"_id": process.ID}
	opts := options.Update().SetUpsert(true)
	res, err := ms.processes.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to create or update process: %w", err)
	}
	if res.UpsertedID == nil {
		return internal.NilObjectID, nil
	}

	return process.ID, nil
}

// DeleteProcess removes a process
func (ms *MongoStorage) DelProcess(processID internal.ObjectID) error {
	if processID == internal.NilObjectID {
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
func (ms *MongoStorage) Process(processID internal.ObjectID) (*Process, error) {
	if processID == internal.NilObjectID {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	process := Process{}
	if err := ms.processes.FindOne(ctx, bson.M{"_id": processID}).Decode(&process); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return &process, nil
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
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}

// ListProcesses retrieves all processes from the DB for an organization
func (ms *MongoStorage) ListProcesses(orgAddress common.Address, page, limit int64, draft DraftFilter) (int64, []Process, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}

	// Create filter - draft processes have nil address, published processes have non-nil address
	filter := bson.M{
		"orgAddress": orgAddress,
	}
	switch draft {
	case DraftOnly:
		filter["address"] = bson.M{"$eq": nil}
	case PublishedOnly:
		filter["address"] = bson.M{"$ne": nil}
	default:
		// no filter
	}

	return paginatedDocuments[Process](ms.processes, page, limit, filter, options.Find())
}

// CountProcesses counts all processes from the DB for an organization
func (ms *MongoStorage) CountProcesses(orgAddress common.Address, draft DraftFilter) (int64, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create filter - draft processes have nil address, published processes have non-nil address
	filter := bson.M{
		"orgAddress": orgAddress,
	}
	switch draft {
	case DraftOnly:
		filter["address"] = bson.M{"$eq": nil}
	case PublishedOnly:
		filter["address"] = bson.M{"$ne": nil}
	default:
		// no filter
	}

	// Count total documents
	return ms.processes.CountDocuments(ctx, filter)
}
