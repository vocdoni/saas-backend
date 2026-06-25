package db

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetProcess creates a new process or updates an existing one for an organization.
// If the process already exists and is in draft mode, it will be updated.
func (ms *MongoStorage) SetProcess(process *Process) (primitive.ObjectID, error) {
	// validate input
	if (process.OrgAddress.Cmp(common.Address{}) == 0) {
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
		if census.OrgAddress.Cmp(process.OrgAddress) != 0 {
			return primitive.NilObjectID, fmt.Errorf("census %s does not belong to organization %s",
				census.ID.Hex(), process.OrgAddress.String())
		}
	}

	if process.ID.IsZero() {
		// if the process doesn't exist, create its id
		process.ID = primitive.NewObjectID()
	}

	updateDoc, err := dynamicUpdateDocument(process, nil)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("failed to create update document: %w", err)
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// Use ReplaceOne with upsert option to either update an existing process or insert a new one
	filter := bson.M{"_id": process.ID}
	opts := options.Update().SetUpsert(true)
	res, err := ms.processes.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("failed to create or update process: %w", err)
	}
	if res.UpsertedID == nil {
		return primitive.NilObjectID, nil
	}

	return process.ID, nil
}

// ClaimProcessForPublish atomically transitions a draft to the PUBLISHING state in a
// single conditional update, but only when it is not already publishing and has not yet
// been published (no on-chain address). It returns true when this call won the claim and
// false when the draft was already publishing or published. This is the authoritative
// duplicate-publish guard: because the check and set are one Mongo operation, two
// concurrent publish requests cannot both proceed and sign two NEW_PROCESS txs.
func (ms *MongoStorage) ClaimProcessForPublish(processID primitive.ObjectID) (bool, error) {
	if processID == primitive.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"_id":     processID,
		"status":  bson.M{"$ne": "PUBLISHING"},
		"address": bson.M{"$eq": nil},
	}
	res, err := ms.processes.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"status": "PUBLISHING"}})
	if err != nil {
		return false, fmt.Errorf("failed to claim process for publish: %w", err)
	}
	return res.ModifiedCount == 1, nil
}

// ClearProcessPublishing reverts a draft from the PUBLISHING state back to an unpublished
// draft (no status). It $unsets the status field rather than writing a zero-value string,
// which dynamicUpdateDocument would silently drop, so a publish that fails after claiming
// the draft cannot leave it permanently stuck in PUBLISHING. The status filter makes it a
// no-op if a worker already advanced the draft to READY.
func (ms *MongoStorage) ClearProcessPublishing(processID primitive.ObjectID) error {
	if processID == primitive.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": processID, "status": "PUBLISHING"}
	if _, err := ms.processes.UpdateOne(ctx, filter, bson.M{"$unset": bson.M{"status": ""}}); err != nil {
		return fmt.Errorf("failed to clear process publishing state: %w", err)
	}
	return nil
}

// SetProcessMetadataURL sets only the metadataURL field of the process identified by
// processID, leaving every other field untouched so a concurrent update to the same
// process (status, publishedAt, counters...) is not clobbered by a stale full rewrite.
func (ms *MongoStorage) SetProcessMetadataURL(processID primitive.ObjectID, metadataURL string) error {
	if processID == primitive.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": processID}
	if _, err := ms.processes.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"metadataURL": metadataURL}}); err != nil {
		return fmt.Errorf("failed to set process metadataURL: %w", err)
	}
	return nil
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
