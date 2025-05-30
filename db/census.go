// Package db provides database operations for the Vocdoni SaaS backend,
// handling storage and retrieval of censuses, organizations, users, and
// other data structures required for the voting platform.
package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// CreateCensus creates a new census for an organization
// Returns the hex representation of the census
func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if census.OrgAddress == "" {
		return "", ErrInvalidData
	}
	// check that the org exists
	_, err := ms.Organization(census.OrgAddress)
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

// DeleteCensus removes a census and all its members
func (ms *MongoStorage) DelCensus(censusID string) error {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// delete the census from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.censuses.DeleteOne(ctx, filter)
	return err
}

// Census retrieves a census from the DB based on its ID
func (ms *MongoStorage) Census(censusID string) (*Census, error) {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return nil, ErrInvalidData
	}

	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	census := &Census{}
	err = ms.censuses.FindOne(ctx, bson.M{"_id": objID}).Decode(census)
	if err != nil {
		return nil, fmt.Errorf("failed to get census: %w", err)
	}

	return census, nil
}

// CensusesByOrg retrieves all the censuses for an organization based on its
// address. It checks that the organization exists and returns an error if it
// doesn't. If the organization exists, it returns the censuses.
func (ms *MongoStorage) CensusesByOrg(orgAddress string) ([]*Census, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := ms.fetchOrganizationFromDB(ctx, orgAddress); err != nil {
		if err == ErrNotFound {
			return nil, ErrInvalidData
		}
		return nil, fmt.Errorf("organization not found: %w", err)
	}
	// find the censuses in the database
	censuses := []*Census{}
	cursor, err := ms.censuses.Find(ctx, bson.M{"orgAddress": orgAddress})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	if err := cursor.All(ctx, &censuses); err != nil {
		return nil, err
	}
	return censuses, nil
}
