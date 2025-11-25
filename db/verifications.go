package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// delVerificationCode private method deletes the verification code for the
// user and type provided. This method must be called with the keysLock held.
func (ms *MongoStorage) delVerificationCode(ctx context.Context, id uint64, t CodeType) error {
	// delete the verification code for the user provided
	_, err := ms.verifications.DeleteOne(ctx, bson.M{"_id": id, "type": t})
	return err
}

// UserVerificationCode returns the verification code for the user provided. If
// the user has not a verification code, it returns an specific error, if other
// error occurs, it returns the error.
func (ms *MongoStorage) UserVerificationCode(user *User, t CodeType) (*UserVerification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
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
func (ms *MongoStorage) SetVerificationCode(user *User, sealedCode []byte, t CodeType, exp time.Time) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// try to get the user to ensure it exists
	if _, err := ms.fetchUserFromDB(ctx, user.ID); err != nil {
		return err
	}
	// insert the verification code for the user provided
	filter := bson.M{"_id": user.ID}
	verification := &UserVerification{
		ID:         user.ID,
		SealedCode: sealedCode,
		Type:       t,
		Expiration: exp,
		Attempts:   1,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := ms.verifications.ReplaceOne(ctx, filter, verification, opts)
	return err
}

// VerificationCodeIncrementAttempts method increments the number of attempts
// for the verification code of the user provided. If an error occurs, it
// returns the error.
func (ms *MongoStorage) VerificationCodeIncrementAttempts(sealedCode []byte, t CodeType) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"sealedCode": sealedCode, "type": t}
	update := bson.M{"$inc": bson.M{"attempts": 1}}
	_, err := ms.verifications.UpdateOne(ctx, filter, update)
	return err
}
