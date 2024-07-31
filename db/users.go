package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (ms *MongoStorage) nextUserID(ctx context.Context) (uint64, error) {
	var user User
	opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
	if err := ms.users.FindOne(ctx, bson.M{}, opts).Decode(&user); err != nil {
		if err == mongo.ErrNoDocuments {
			return 1, nil
		} else {
			return 0, err
		}
	}
	return user.ID + 1, nil
}

func (ms *MongoStorage) UserByEmail(email string) (*User, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.users.FindOne(ctx, bson.M{"email": email})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

func (ms *MongoStorage) User(id uint64) (*User, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.users.FindOne(ctx, bson.M{"_id": id})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

func (ms *MongoStorage) SetUser(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// check if the user exists or needs to be created
	if user.ID != 0 {
		// if the user exists, update it with the new data
		updateDoc, err := dynamicUpdateDocument(user, nil)
		if err != nil {
			return err
		}
		_, err = ms.users.UpdateOne(ctx, bson.M{"_id": user.ID}, updateDoc)
		if err != nil {
			return err
		}
	} else {
		// if the user doesn't exist, create it setting the ID first
		var err error
		if user.ID, err = ms.nextUserID(ctx); err != nil {
			return err
		}
		if _, err := ms.users.InsertOne(ctx, user); err != nil {
			return err
		}
	}

	return nil
}

func (ms *MongoStorage) DelUser(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := ms.users.DeleteOne(ctx, bson.M{"_id": user.ID})
	return err
}
