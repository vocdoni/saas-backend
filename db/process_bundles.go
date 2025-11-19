package db

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetProcessBundle creates a new process bundle or updates an existing one.
// It validates that the organization and census exist before creating or updating the bundle.
// Returns the bundle ID as a hex string on success.
func (ms *MongoStorage) SetProcessBundle(bundle *ProcessesBundle) (internal.ObjectID, error) {
	if bundle.ID.IsZero() {
		bundle.ID = internal.NewObjectID()
	}

	// Check that the org exists
	if _, err := ms.Organization(bundle.OrgAddress); err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to get organization: %w", err)
	}

	// check that the census exists
	_, err := ms.Census(bundle.Census.ID)
	if err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to get census: %w", err)
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// If the bundle has an ID, update it, otherwise create a new one
	filter := bson.M{"_id": bundle.ID}
	update := bson.M{"$set": bundle}
	opts := &options.UpdateOptions{}
	opts.SetUpsert(true)

	if _, err := ms.processBundles.UpdateOne(ctx, filter, update, opts); err != nil {
		return internal.NilObjectID, fmt.Errorf("failed to update process bundle: %w", err)
	}

	return bundle.ID, nil
}

// DelProcessBundle removes a process bundle by ID.
// Returns ErrInvalidData if the bundleID is zero, or ErrNotFound if no bundle with the given ID exists.
func (ms *MongoStorage) DelProcessBundle(bundleID internal.ObjectID) error {
	if bundleID.IsZero() {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Delete the process bundle from the database using the ID
	filter := bson.M{"_id": bundleID}
	result, err := ms.processBundles.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to delete process bundle: %w", err)
	}

	if result.DeletedCount == 0 {
		return ErrNotFound
	}

	return nil
}

// ProcessBundle retrieves a process bundle from the database based on its ID.
// Returns the bundle with all its associated data including census information and processes.
func (ms *MongoStorage) ProcessBundle(bundleID internal.ObjectID) (*ProcessesBundle, error) {
	if bundleID.IsZero() {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	bundle := &ProcessesBundle{}
	if err := ms.processBundles.FindOne(ctx, bson.M{"_id": bundleID}).Decode(bundle); err != nil {
		return nil, fmt.Errorf("failed to get process bundle: %w", err)
	}

	return bundle, nil
}

// ProcessBundles retrieves all process bundles from the database.
// Returns a slice of all process bundles with their complete information.
func (ms *MongoStorage) ProcessBundles() ([]*ProcessesBundle, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	cursor, err := ms.processBundles.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to find process bundles: %w", err)
	}
	defer func() {
		err := cursor.Close(ctx)
		if err != nil {
			fmt.Println("failed to close cursor")
		}
	}()

	var bundles []*ProcessesBundle
	if err := cursor.All(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("failed to decode process bundles: %w", err)
	}

	return bundles, nil
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
		err := cursor.Close(ctx)
		if err != nil {
			fmt.Println("failed to close cursor")
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
		err := cursor.Close(ctx)
		if err != nil {
			fmt.Println("failed to close cursor")
		}
	}()

	var bundles []*ProcessesBundle
	if err := cursor.All(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("failed to decode process bundles: %w", err)
	}

	return bundles, nil
}

// AddProcessesToBundle adds processes to an existing bundle if they don't already exist.
// It checks each process to avoid duplicates and only updates the database if new processes were added.
func (ms *MongoStorage) AddProcessesToBundle(bundleID internal.ObjectID, processes []internal.HexBytes) error {
	if bundleID.IsZero() {
		return ErrInvalidData
	}
	if len(processes) == 0 {
		return ErrInvalidData
	}

	bundle, err := ms.ProcessBundle(bundleID)
	if err != nil {
		return fmt.Errorf("failed to get process bundle: %w", err)
	}

	if bundle.ID.IsZero() {
		bundle.ID = internal.NewObjectID()
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// Check each process and add it if it doesn't already exist in the bundle
	processesAdded := false
	for _, newProcess := range processes {
		exists := false
		for _, existingProcess := range bundle.Processes {
			if bytes.Equal(existingProcess, newProcess) {
				exists = true
				break
			}
		}
		if !exists {
			bundle.Processes = append(bundle.Processes, newProcess)
			processesAdded = true
		}
	}

	// If no processes were added, return early
	if !processesAdded {
		return nil
	}

	// Update the bundle in the database
	filter := bson.M{"_id": bundleID}
	update := bson.M{"$set": bson.M{"processes": bundle.Processes}}
	if _, err := ms.processBundles.UpdateOne(ctx, filter, update); err != nil {
		return fmt.Errorf("failed to update process bundle: %w", err)
	}

	return nil
}

// NewBundleID generates a new unique ObjectID for a process bundle.
// This is used when creating a new bundle to ensure it has a unique identifier.
func (*MongoStorage) NewBundleID() internal.ObjectID {
	return internal.NewObjectID()
}
