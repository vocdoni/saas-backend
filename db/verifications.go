package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// delVerificationCode private method deletes the verification code for the
// user and type provided.
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var user *User
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		result := ms.verifications.FindOne(sessCtx, bson.M{"code": code, "type": t})
		verification := &UserVerification{}
		if err := result.Decode(verification); err != nil {
			if err == mongo.ErrNoDocuments {
				return ErrNotFound
			}
			return err
		}

		var fetchErr error
		user, fetchErr = ms.fetchUserFromDB(sessCtx, verification.ID)
		return fetchErr
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// UserVerificationCode returns the verification code for the user provided. If
// the user has not a verification code, it returns an specific error, if other
// error occurs, it returns the error.
func (ms *MongoStorage) UserVerificationCode(user *User, t CodeType) (*UserVerification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read operations don't need transactions, but we'll use a session for consistency
	session, err := ms.DBClient.StartSession()
	if err != nil {
		return nil, fmt.Errorf("failed to start session: %w", err)
	}
	defer session.EndSession(ctx)

	var verification *UserVerification
	err = mongo.WithSession(ctx, session, func(sessCtx mongo.SessionContext) error {
		result := ms.verifications.FindOne(sessCtx, bson.M{"_id": user.ID, "type": t})
		verification = &UserVerification{}
		if err := result.Decode(verification); err != nil {
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
	return verification, nil
}

// SetVerificationCode method sets the verification code for the user provided.
// If the user already has a verification code, it updates it. If an error
// occurs, it returns the error.
func (ms *MongoStorage) SetVerificationCode(user *User, code string, t CodeType, exp time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Execute the operation within a transaction
	return ms.WithTransaction(ctx, func(sessCtx mongo.SessionContext) error {
		// Try to get the user to ensure it exists
		if _, err := ms.fetchUserFromDB(sessCtx, user.ID); err != nil {
			return err
		}

		// Insert the verification code for the user provided
		filter := bson.M{"_id": user.ID, "type": t}
		verification := &UserVerification{
			ID:         user.ID,
			Code:       code,
			Type:       t,
			Expiration: exp,
		}
		opts := options.Replace().SetUpsert(true)
		_, err := ms.verifications.ReplaceOne(sessCtx, filter, verification, opts)
		return err
	})
}
