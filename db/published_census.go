package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CreatePublishedCensus creates a new publishedCensus for an organization
func (ms *MongoStorage) SetPublishedCensus(publishedCensus *PublishedCensus) error {
	if len(publishedCensus.URI) == 0 || len(publishedCensus.Root) == 0 || publishedCensus.Census.ID == primitive.NilObjectID {
		return ErrInvalidData
	}

	census, err := ms.Census(publishedCensus.Census.ID.Hex())
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	publishedCensus.Census = *census
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// TODO do no recreate the publishedCensus if it already exists
	publishedCensus.CreatedAt = time.Now()
	if _, err := ms.publishedCensuses.InsertOne(ctx, publishedCensus); err != nil {
		return fmt.Errorf("failed to create publishedCensus: %w", err)
	}

	return nil
}

// DeletePublishedCensus removes a publishedCensus and all its participants
func (ms *MongoStorage) DelPublishedCensus(root, uri string) error {
	if len(uri) == 0 || len(root) == 0 {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the publishedCensus from the database using the ID
	filter := bson.M{"root": root, "uri": uri}
	_, err := ms.publishedCensuses.DeleteOne(ctx, filter)
	return err
}

// PublishedCensus retrieves a publishedCensus from the DB based on it ID
func (ms *MongoStorage) PublishedCensus(root, uri, censusId string) (*PublishedCensus, error) {
	if len(uri) == 0 || len(root) == 0 {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	publishedCensus := &PublishedCensus{}
	censusOID, err := primitive.ObjectIDFromHex(censusId)
	if err != nil {
		return nil, ErrInvalidData
	}
	filter := bson.M{"root": root, "uri": uri, "census._id": censusOID}
	if err := ms.publishedCensuses.FindOne(ctx, filter).Decode(publishedCensus); err != nil {
		return nil, ErrNotFound
	}

	return publishedCensus, nil
}
