package db

import (
	"context"
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// SetProcess creates a new process or updates an existing one for an organization.
// If the process already exists and is in draft mode, it will be updated.
func (ms *MongoStorage) SetProcess(process *Process) (string, error) {
	// validate input
	// either OrgAddress or Census.ID cannot be empty in order to connect to an organization
	if (process.OrgAddress.Cmp(common.Address{}) == 0) {
		return "", ErrInvalidData
	}

	// check that the org exists
	if _, err := ms.Organization(process.OrgAddress); err != nil {
		return "", fmt.Errorf("failed to get organization %s: %w", process.OrgAddress, err)
	}
	// check that the census exists
	if !process.Census.ID.IsZero() {
		census, err := ms.Census(process.Census.ID.Hex())
		if err != nil {
			return "", fmt.Errorf("failed to get census: %w", err)
		}
		if len(census.Published.Root) == 0 || len(census.Published.URI) == 0 {
			return "", fmt.Errorf("census %s does not have a published root or URI", census.ID.Hex())
		}
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// Use ReplaceOne with upsert option to either update an existing process or insert a new one
	var filter bson.M
	if !process.ID.IsZero() {
		filter = bson.M{"_id": process.ID}
	}
	opts := options.Replace().SetUpsert(true)
	res, err := ms.processes.ReplaceOne(ctx, filter, process, opts)
	if err != nil {
		return "", fmt.Errorf("failed to create or update process: %w", err)
	}

	return res.UpsertedID.(primitive.ObjectID).Hex(), nil
}

// DeleteProcess removes a process
func (ms *MongoStorage) DelProcess(processID string) error {
	if len(processID) == 0 {
		return ErrInvalidData
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the process from the database using the ID
	filter := bson.M{"_id": parsedID}
	_, err = ms.processes.DeleteOne(ctx, filter)
	return err
}

// / Process retrieves a process from the DB based on its ID
func (ms *MongoStorage) Process(processID string) (*Process, error) {
	if processID == "" {
		return nil, ErrInvalidData
	}

	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	process := &Process{}
	if err := ms.processes.FindOne(ctx, bson.M{"_id": parsedID}).Decode(process); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process: %w", err)
	}

	return process, nil
}

// Process retrieves a process from the DB based on its ID
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
func (ms *MongoStorage) ListProcesses(orgAddress common.Address, page, pageSize int, draft bool) (int, []Process, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Create filter
	filter := bson.M{
		"orgAddress": orgAddress,
		"draft":      draft,
	}

	// Calculate skip value based on page and pageSize
	skip := (page - 1) * pageSize

	// Count total documents
	totalCount, err := ms.processes.CountDocuments(ctx, filter)
	if err != nil {
		return 0, nil, err
	}
	totalPages := int(math.Ceil(float64(totalCount) / float64(pageSize)))
	sort := bson.D{
		bson.E{Key: "_id", Value: 1},
	}
	// Set up options for pagination
	findOptions := options.Find().
		SetSort(sort). // Sort by _id in descending order
		SetSkip(int64(skip)).
		SetLimit(int64(pageSize))

	// Execute the find operation with pagination
	cursor, err := ms.processes.Find(ctx, filter, findOptions)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to get processes: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	// Decode results
	var processes []Process
	if err = cursor.All(ctx, &processes); err != nil {
		return 0, nil, fmt.Errorf("failed to decode processes: %w", err)
	}

	return totalPages, processes, nil
}
