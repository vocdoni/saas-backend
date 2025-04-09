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
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// SetCensus creates a new census for an organization
// Returns the hex representation of the census
func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
	if census.OrgAddress == "" {
		return "", ErrInvalidData
	}

	// Check that the org exists before starting the transaction
	_, err := ms.Organization(census.OrgAddress)
	if err != nil {
		if err == ErrNotFound {
			return "", ErrInvalidData
		}
		return "", fmt.Errorf("organization not found: %w", err)
	}

	// Set timestamps and ID
	if census.ID != primitive.NilObjectID {
		// If the census exists, update it with the new data
		census.UpdatedAt = time.Now()
	} else {
		// If the census doesn't exist, create its id
		census.ID = primitive.NewObjectID()
		census.CreatedAt = time.Now()
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	err = ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		updateDoc, err := dynamicUpdateDocument(census, nil)
		if err != nil {
			return err
		}

		filter := bson.M{"_id": census.ID}
		opts := options.Update().SetUpsert(true)
		_, err = ms.censuses.UpdateOne(sessCtx, filter, updateDoc, opts)
		return err
	})
	if err != nil {
		return "", err
	}
	return census.ID.Hex(), nil
}

// DelCensus removes a census and all its participants
func (ms *MongoStorage) DelCensus(censusID string) error {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the census from the database using the ID
		filter := bson.M{"_id": objID}
		_, err = ms.censuses.DeleteOne(sessCtx, filter)
		return err
	})
}

// Census retrieves a census from the DB based on it ID
func (ms *MongoStorage) Census(censusID string) (*Census, error) {
	objID, err := primitive.ObjectIDFromHex(censusID)
	if err != nil {
		return nil, ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var census *Census
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		census = &Census{}
		err = ms.censuses.FindOne(sessCtx, bson.M{"_id": objID}).Decode(census)
		if err != nil {
			return fmt.Errorf("failed to get census: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return census, nil
}

// CensusesByOrg retrieves all the censuses for an organization based on its
// address. It checks that the organization exists and returns an error if it
// doesn't. If the organization exists, it returns the censuses.
func (ms *MongoStorage) CensusesByOrg(orgAddress string) ([]*Census, error) {
	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var censuses []*Census
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Check that the organization exists
		if _, err := ms.fetchOrganizationFromDB(sessCtx, orgAddress); err != nil {
			if err == ErrNotFound {
				return ErrInvalidData
			}
			return fmt.Errorf("organization not found: %w", err)
		}

		// Find the censuses in the database
		cursor, err := ms.censuses.Find(sessCtx, bson.M{"orgAddress": orgAddress})
		if err != nil {
			return err
		}
		defer func() {
			if err := cursor.Close(sessCtx); err != nil {
				log.Warnw("error closing cursor", "error", err)
			}
		}()

		censuses = []*Census{}
		return cursor.All(sessCtx, &censuses)
	})
	if err != nil {
		return nil, err
	}
	return censuses, nil
}
