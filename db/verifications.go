package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// userCode private method returns the user verification with the given ID and
// type. If the verification doesn't exist, it returns a specific error. If
// other errors occur, it returns the error. This method must be called with
// the keysLock held.
func (ms *MongoStorage) userCode(ctx context.Context, id uint64, t CodeType) (*UserVerification, error) {
	result := ms.verifications.FindOne(ctx, bson.M{"_id": id, "type": t})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return verification, nil
}

// delVerificationCode private method deletes the verification code for the
// user and type provided. This method must be called with the keysLock held.
func (ms *MongoStorage) delVerificationCode(ctx context.Context, id uint64, t CodeType) error {
	// delete the verification code for the user provided
	_, err := ms.verifications.DeleteOne(ctx, bson.M{"_id": id, "type": t})
	return err
}

// UserByVerificationCode method returns the user with the given verification
// code. If the user or the verification code doesn't exist, it returns a
// specific error. If other errors occur, it returns the error. It checks the
// user verification code in the verifications collection and returns the user
// with the ID associated with the verification code.
func (ms *MongoStorage) UserByVerificationCode(code string, t CodeType) (*User, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.verifications.FindOne(ctx, bson.M{"code": code, "type": t})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return ms.user(ctx, verification.ID)
}

// UserVerificationCode returns the verification code for the user provided. If
// the user has not a verification code, it returns an specific error, if other
// error occurs, it returns the error.
func (ms *MongoStorage) UserVerificationCode(user *User, t CodeType) (*UserVerification, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ms.verifications.FindOne(ctx, bson.M{"_id": user.ID, "type": t})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return verification, nil
}

// SetVerificationCode method sets the verification code for the user provided.
// If the user already has a verification code, it updates it. If an error
// occurs, it returns the error.
func (ms *MongoStorage) SetVerificationCode(user *User, code string, t CodeType, exp time.Time) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// try to get the user to ensure it exists
	if _, err := ms.user(ctx, user.ID); err != nil {
		return err
	}
	// insert the verification code for the user provided
	filter := bson.M{"_id": user.ID}
	verification := &UserVerification{
		ID:         user.ID,
		Code:       code,
		Type:       t,
		Expiration: exp,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := ms.verifications.ReplaceOne(ctx, filter, verification, opts)
	return err
}

// DelVerificationCode method deletes the verification code for the user and
// type provided. If the code doesn't exist, it returns an specific error. If
// other error occurs, it returns the error.
func (ms *MongoStorage) DelUserVerificationCode(user *User, t CodeType) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// check if the verification code exists
	if _, err := ms.userCode(ctx, user.ID, t); err != nil {
		return err
	}
	// delete the verification code for the user provided
	return ms.delVerificationCode(ctx, user.ID, t)
}
