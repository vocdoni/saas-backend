package db

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Process retrieves a process from the DB based on its ID
func (ms *MongoStorage) Process(processID bson.ObjectID) (*Process, error) {
	if processID == bson.NilObjectID {
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

// AllProcessesByOrg returns every process owned by the given organization without pagination,
// filtered by the draft filter. It is used where the caller must inspect the full set (e.g. the
// managed-org teardown guard, which must check every published election for an active status
// rather than only the first page).
func (ms *MongoStorage) AllProcessesByOrg(orgAddress common.Address, draft DraftFilter) ([]Process, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"orgAddress": orgAddress}
	switch draft {
	case DraftOnly:
		filter["address"] = bson.M{"$eq": nil}
	case PublishedOnly:
		filter["address"] = bson.M{"$ne": nil}
	default:
		// no filter
	}
	cursor, err := ms.processes.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch processes by org: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	var out []Process
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("failed to decode processes: %w", err)
	}
	return out, nil
}

// DeleteProcessesByOrg removes every process (drafts and published DB rows) owned by the
// given organization. On-chain elections are immutable on the Vochain and are not affected.
// Returns the number of deleted process documents.
func (ms *MongoStorage) DeleteProcessesByOrg(orgAddress common.Address) (int64, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processes.DeleteMany(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return 0, fmt.Errorf("failed to delete processes by org: %w", err)
	}
	return res.DeletedCount, nil
}
