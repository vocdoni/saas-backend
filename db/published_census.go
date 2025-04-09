package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// SetPublishedCensus creates a new publishedCensus for an organization
func (ms *MongoStorage) SetPublishedCensus(publishedCensus *PublishedCensus) error {
	if len(publishedCensus.URI) == 0 || len(publishedCensus.Root) == 0 || publishedCensus.Census.ID == primitive.NilObjectID {
		return ErrInvalidData
	}

	// Get the census before starting the transaction
	census, err := ms.Census(publishedCensus.Census.ID.Hex())
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	publishedCensus.Census = *census

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// TODO do not recreate the publishedCensus if it already exists
		publishedCensus.CreatedAt = time.Now()
		if _, err := ms.publishedCensuses.InsertOne(sessCtx, publishedCensus); err != nil {
			return fmt.Errorf("failed to create publishedCensus: %w", err)
		}
		return nil
	})
}

// DelPublishedCensus removes a publishedCensus and all its participants
func (ms *MongoStorage) DelPublishedCensus(root, uri string) error {
	if len(uri) == 0 || len(root) == 0 {
		return ErrInvalidData
	}

	// Create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Delete the publishedCensus from the database using the ID
		filter := bson.M{"root": root, "uri": uri}
		_, err := ms.publishedCensuses.DeleteOne(sessCtx, filter)
		return err
	})
}

// PublishedCensus retrieves a publishedCensus from the DB based on it ID
func (ms *MongoStorage) PublishedCensus(root, uri, censusID string) (*PublishedCensus, error) {
	if len(uri) == 0 || len(root) == 0 {
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

	var publishedCensus *PublishedCensus
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		censusOID, err := primitive.ObjectIDFromHex(censusID)
		if err != nil {
			return ErrInvalidData
		}

		filter := bson.M{"root": root, "uri": uri, "census._id": censusOID}
		publishedCensus = &PublishedCensus{}
		if err := ms.publishedCensuses.FindOne(sessCtx, filter).Decode(publishedCensus); err != nil {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return publishedCensus, nil
}
