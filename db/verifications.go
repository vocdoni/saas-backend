package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (ms *MongoStorage) UserVerificationCode(user *User) (string, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.verifications.FindOne(ctx, bson.M{"_id": user.ID})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return "", ErrNotFound
		}
		return "", err
	}
	return verification.Code, nil
}

func (ms *MongoStorage) SetVerificationCode(user *User, code string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// insert the verification code for the user provided
	filter := bson.M{"_id": user.ID}
	verification := &UserVerification{
		ID:   user.ID,
		Code: code,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := ms.verifications.ReplaceOne(ctx, filter, verification, opts)
	return err
}

func (ms *MongoStorage) VerifyUser(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// update the user to mark as verified
	filter := bson.M{"_id": user.ID}
	if _, err := ms.users.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"verified": true}}); err != nil {
		return err
	}
	// remove the verification code
	_, err := ms.verifications.DeleteOne(ctx, bson.M{"_id": user.ID})
	return err
}
