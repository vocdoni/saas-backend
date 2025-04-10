package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// The Object entity represents a generic object stored in the database
// intended for s3-like storage.

// Object retrieves an object from the MongoDB collection by its ID.
func (ms *MongoStorage) Object(id string) (*Object, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var obj *Object
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		// Find the object in the database
		result := ms.objects.FindOne(sessCtx, bson.M{"_id": id})
		obj = &Object{}
		if err := result.Decode(obj); err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// SetObject sets the object data for the given objectID. If the
// object does not exist, it will be created with the given data, otherwise it
// will be updated.
func (ms *MongoStorage) SetObject(objectID, userID, contentType string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		object := &Object{
			ID:          objectID,
			Data:        data,
			CreatedAt:   time.Now(),
			UserID:      userID,
			ContentType: contentType,
		}
		opts := options.ReplaceOptions{}
		opts.Upsert = new(bool)
		*opts.Upsert = true
		_, err := ms.objects.ReplaceOne(sessCtx, bson.M{"_id": object.ID}, object, &opts)
		if err != nil {
			return fmt.Errorf("cannot update object: %w", err)
		}
		return nil
	})
}

// RemoveObject removes the object data for the given objectID.
func (ms *MongoStorage) RemoveObject(objectID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		_, err := ms.objects.DeleteOne(sessCtx, bson.M{"_id": objectID})
		return err
	})
}
