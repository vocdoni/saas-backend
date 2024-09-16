package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// UserVerificationCode returns the verification code for the user provided. If
// the user has not a verification code, it returns an specific error, if other
// error occurs, it returns the error.
func (ms *MongoStorage) UserVerificationCode(user *User, t CodeType) (string, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.verifications.FindOne(ctx, bson.M{"_id": user.ID, "type": t})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return "", ErrNotFound
		}
		return "", err
	}
	return verification.Code, nil
}

// SetVerificationCode method sets the verification code for the user provided.
// If the user already has a verification code, it updates it. If an error
// occurs, it returns the error.
func (ms *MongoStorage) SetVerificationCode(user *User, code string, t CodeType) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// try to get the user to ensure it exists
	if _, err := ms.user(user.ID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// insert the verification code for the user provided
	filter := bson.M{"_id": user.ID}
	verification := &UserVerification{
		ID:   user.ID,
		Code: code,
		Type: t,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := ms.verifications.ReplaceOne(ctx, filter, verification, opts)
	return err
}

// VerifyUserAccount method verifies the user provided, modifying the user to
// mark as verified and removing the verification code. If an error occurs, it
// returns the error.
func (ms *MongoStorage) VerifyUserAccount(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// try to get the user to ensure it exists
	if _, err := ms.user(user.ID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// update the user to mark as verified
	filter := bson.M{"_id": user.ID}
	if _, err := ms.users.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"verified": true}}); err != nil {
		return err
	}
	// remove the verification code
	_, err := ms.verifications.DeleteOne(ctx, bson.M{"_id": user.ID, "type": CodeTypeAccountVerification})
	return err
}
