package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CreateCensus creates a new census for an organization
// Returns the hex representation of the census
func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if census.OrgAddress == "" {
		return "", ErrInvalidData
	}
	// check that the org exists
	_, _, err := ms.Organization(census.OrgAddress, false)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	if census.ID != primitive.NilObjectID {
		// if the census exists, update it with the new data
		census.UpdatedAt = time.Now()
	} else {
		// if the census doesn't exist, create its id
		census.ID = primitive.NewObjectID()
		census.CreatedAt = time.Now()
	}

	updateDoc, err := dynamicUpdateDocument(census, nil)
	if err != nil {
		return "", err
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	filter := bson.M{"_id": census.ID}
	opts := options.Update().SetUpsert(true)
	_, err = ms.censuses.UpdateOne(ctx, filter, updateDoc, opts)
	if err != nil {
		return "", err
	}

	return census.ID.Hex(), nil
}

// DeleteCensus removes a census and all its participants
func (ms *MongoStorage) DelCensus(censusID string) error {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the census from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.censuses.DeleteOne(ctx, filter)
	return err
}

// Census retrieves a census from the DB based on it ID
func (ms *MongoStorage) Census(censusID string) (*Census, error) {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	census := &Census{}
	err = ms.censuses.FindOne(ctx, bson.M{"_id": objID}).Decode(census)
	if err != nil {
		return nil, fmt.Errorf("failed to get census: %w", err)
	}

	return census, nil
}
