package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CreateCensus creates a new census for an organization
// Returns the hex representation of the census
func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var oid primitive.ObjectID

	if census.OrgAddress == "" {
		return "", ErrInvalidData
	}
	// check that the org exists
	result := ms.organizations.FindOne(ctx, bson.M{"_id": census.OrgAddress})
	if result.Err() != nil {
		return "", fmt.Errorf("organization not found: %w", result.Err())
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	if census.ID != primitive.NilObjectID {
		oid = census.ID
		census.UpdatedAt = time.Now()
		// if the census exists, update it with the new data
		updateDoc, err := dynamicUpdateDocument(census, nil)
		if err != nil {
			return "", err
		}
		_, err = ms.censuses.UpdateOne(ctx, bson.M{"_id": census.ID}, updateDoc)
		if err != nil {
			return "", err
		}
	} else {
		// if the census doesn't exist, create it
		census.CreatedAt = time.Now()
		result, err := ms.censuses.InsertOne(ctx, census)
		if err != nil {
			return "", fmt.Errorf("failed to create census: %w", err)
		}
		oid = result.InsertedID.(primitive.ObjectID)
	}

	return oid.Hex(), nil
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
