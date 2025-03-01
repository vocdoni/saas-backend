package db

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetProcessBundle creates a new process bundle or updates an existing one
func (ms *MongoStorage) SetProcessBundle(bundle *ProcessesBundle) (string, error) {
	if bundle.ID.IsZero() {
		bundle.ID = primitive.NewObjectID()
	}

	// Check that the org exists
	if _, _, err := ms.Organization(bundle.OrgAddress, false); err != nil {
		return "", fmt.Errorf("failed to get organization: %w", err)
	}

	// check that the census exists
	_, err := ms.Census(bundle.Census.ID.Hex())
	if err != nil {
		return "", fmt.Errorf("failed to get census: %w", err)
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// If the bundle has an ID, update it, otherwise create a new one
	if bundle.ID.IsZero() {
		if _, err := ms.processBundles.InsertOne(ctx, bundle); err != nil {
			return "", fmt.Errorf("failed to create process bundle: %w", err)
		}
	} else {
		filter := bson.M{"_id": bundle.ID}
		update := bson.M{"$set": bundle}
		options := &options.UpdateOptions{}
		options.SetUpsert(true)

		if _, err := ms.processBundles.UpdateOne(ctx, filter, update, options); err != nil {
			return "", fmt.Errorf("failed to update process bundle: %w", err)
		}
	}

	return bundle.ID.Hex(), nil
}

// DelProcessBundle removes a process bundle by ID
func (ms *MongoStorage) DelProcessBundle(bundleID primitive.ObjectID) error {
	if bundleID.IsZero() {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// ProcessBundle retrieves a process bundle from the DB based on its ID
func (ms *MongoStorage) ProcessBundle(bundleID primitive.ObjectID) (*ProcessesBundle, error) {
	if bundleID.IsZero() {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bundle := &ProcessesBundle{}
	if err := ms.processBundles.FindOne(ctx, bson.M{"_id": bundleID}).Decode(bundle); err != nil {
		return nil, fmt.Errorf("failed to get process bundle: %w", err)
	}

	return bundle, nil
}

// ProcessBundles retrieves all process bundles
func (ms *MongoStorage) ProcessBundles() ([]*ProcessesBundle, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// ProcessBundlesByProcess retrieves process bundles that contain a specific process ID
func (ms *MongoStorage) ProcessBundlesByProcess(processID []byte) ([]*ProcessesBundle, error) {
	if len(processID) == 0 {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Find bundles where the processes array contains a process with the given ID
	filter := bson.M{"processes._id": processID}
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

// ProcessBundlesByOrg retrieves process bundles that belong to a specific organization
func (ms *MongoStorage) ProcessBundlesByOrg(orgAddress string) ([]*ProcessesBundle, error) {
	if len(orgAddress) == 0 {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// AddProcessesToBundle adds processes to an existing bundle if they don't already exist
func (ms *MongoStorage) AddProcessesToBundle(bundleID primitive.ObjectID, processes []internal.HexBytes) error {
	if bundleID.IsZero() || len(processes) == 0 {
		return ErrInvalidData
	}

	bundle, err := ms.ProcessBundle(bundleID)
	if err != nil {
		return fmt.Errorf("failed to get process bundle: %w", err)
	}

	if bundle.ID.IsZero() {
		bundle.ID = primitive.NewObjectID()
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

func (ms *MongoStorage) NewBundleID() primitive.ObjectID {
	return primitive.NewObjectID()
}
