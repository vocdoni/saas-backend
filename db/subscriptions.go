package db

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextSubscriptionID internal method returns the next available subsbscription ID. If an error
// occurs, it returns the error. This method must be called with the keysLock
// held.
func (ms *MongoStorage) nextSubscriptionID(ctx context.Context) (uint64, error) {
	var subscription Subscription
	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	if err := ms.subscriptions.FindOne(ctx, bson.M{}, opts).Decode(&subscription); err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		} else {
			return 0, err
		}
	}
	return subscription.ID + 1, nil
}

// SetSubscription method creates or updates the subscription in the database.
// If the subscription already exists, it updates the fields that have changed.
func (ms *MongoStorage) SetSubscription(subscription *Subscription) (uint64, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nextID, err := ms.nextSubscriptionID(ctx)
	if err != nil {
		return 0, err
	}
	if subscription.ID > 0 {
		if subscription.ID >= nextID {
			return 0, ErrInvalidData
		}
		updateDoc, err := dynamicUpdateDocument(subscription, nil)
		if err != nil {
			return 0, err
		}
		// set upsert to true to create the document if it doesn't exist
		if _, err := ms.subscriptions.UpdateOne(ctx, bson.M{"_id": subscription.ID}, updateDoc); err != nil {
			return 0, err
		}
	} else {
		subscription.ID = nextID
		if _, err := ms.subscriptions.InsertOne(ctx, subscription); err != nil {
			return 0, err
		}
	}
	return subscription.ID, nil
}

// Subscription method returns the subscription with the given ID. If the
// subscription doesn't exist, it returns the specific error.
func (ms *MongoStorage) Subscription(subscriptionID uint64) (*Subscription, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find the subscription in the database
	filter := bson.M{"_id": subscriptionID}
	subscription := &Subscription{}
	err := ms.subscriptions.FindOne(ctx, filter).Decode(subscription)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound // Subscription not found
		}
		return nil, errors.New("failed to get subscription")
	}
	return subscription, nil
}

// Subscriptions method returns all subscriptions from the database.
func (ms *MongoStorage) Subscriptions() ([]*Subscription, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// find all subscriptions in the database
	cursor, err := ms.subscriptions.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("failed to close subscriptions file", "error", err)
		}
	}()

	// iterate over the cursor and decode each subscription
	var subscriptions []*Subscription
	for cursor.Next(ctx) {
		subscription := &Subscription{}
		if err := cursor.Decode(subscription); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return subscriptions, nil
}

// DelSubscription method deletes the subscription with the given ID. If the
// subscription doesn't exist, it returns the specific error.
func (ms *MongoStorage) DelSubscription(subscription *Subscription) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// delete the organization from the database
	_, err := ms.subscriptions.DeleteOne(ctx, bson.M{"_id": subscription.ID})
	return err
}
