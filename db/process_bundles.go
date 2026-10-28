package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

// ProcessBundle retrieves a process bundle from the database based on its ID.
// Returns the bundle with all its associated data including census information and processes.
func (ms *MongoStorage) ProcessBundle(hbBundleID internal.HexBytes) (*ProcessesBundle, error) {
	bundleID, err := bson.ObjectIDFromHex(hbBundleID.String())
	if err != nil {
		return nil, ErrInvalidData
	}
	if bundleID.IsZero() {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	bundle := &ProcessesBundle{}
	if err := ms.processBundles.FindOne(ctx, bson.M{"_id": bundleID}).Decode(bundle); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process bundle: %w", err)
	}

	return bundle, nil
}

// ProcessBundlesByProcess retrieves process bundles that contain a specific process ID.
// This allows finding all bundles that include a particular process.
func (ms *MongoStorage) ProcessBundlesByProcess(processID internal.HexBytes) ([]*ProcessesBundle, error) {
	if len(processID) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Find bundles where the processes array contains a process with the given ID
	filter := bson.M{"processes": processID}
	cursor, err := ms.processBundles.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to find process bundles by process ID: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("failed to close cursor", "error", err)
		}
	}()

	var bundles []*ProcessesBundle
	if err := cursor.All(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("failed to decode process bundles: %w", err)
	}

	return bundles, nil
}

// ProcessBundlesByOrg retrieves process bundles that belong to a specific organization.
// This allows finding all bundles created by a particular organization.
func (ms *MongoStorage) ProcessBundlesByOrg(orgAddress common.Address) ([]*ProcessesBundle, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Find bundles where the orgAddress matches the given address
	filter := bson.M{"orgAddress": orgAddress}
	cursor, err := ms.processBundles.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to find process bundles by organization: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("failed to close cursor", "error", err)
		}
	}()

	var bundles []*ProcessesBundle
	if err := cursor.All(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("failed to decode process bundles: %w", err)
	}

	return bundles, nil
}

// DeleteProcessBundlesByOrg removes every process bundle owned by the given organization.
// Best-effort cleanup used when tearing down an organization. Returns the number of
// deleted bundles. CSP auth tokens tied to those bundles must be cleaned up separately
// (DeleteCSPAuthByAnchor) before or after this call.
func (ms *MongoStorage) DeleteProcessBundlesByOrg(orgAddress common.Address) (int64, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	res, err := ms.processBundles.DeleteMany(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return 0, fmt.Errorf("failed to delete process bundles by org: %w", err)
	}
	return res.DeletedCount, nil
}
