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
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	publishedCensus.CreatedAt = time.Now()
	if _, err := ms.publishedCensuses.InsertOne(ctx, publishedCensus); err != nil {
		return fmt.Errorf("failed to create publishedCensus: %w", err)
	}

	return nil
}

// DeletePublishedCensus removes a publishedCensus and all its participants
func (ms *MongoStorage) DelPublishedCensus(publishedCensusID string) error {
	objID, err := primitive.ObjectIDFromHex(publishedCensusID)
	if err != nil {
		return ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// delete the publishedCensus from the database using the ID
	filter := bson.M{"_id": objID}
	_, err = ms.publishedCensuses.DeleteOne(ctx, filter)
	return err
}

// PublishedCensus retrieves a publishedCensus from the DB based on it ID
func (ms *MongoStorage) PublishedCensus(publishedCensusID string) (*PublishedCensus, error) {
	objID, err := primitive.ObjectIDFromHex(publishedCensusID)
	if err != nil {
		return nil, ErrInvalidData
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	publishedCensus := &PublishedCensus{}
	err = ms.publishedCensuses.FindOne(ctx, bson.M{"_id": objID}).Decode(publishedCensus)
	if err != nil {
		return nil, fmt.Errorf("failed to get publishedCensus: %w", err)
	}

	return publishedCensus, nil
}
