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

// DeleteUserVerificationCode deletes the verification code of the given type for
// the user provided. It is safe to call when no code exists. If an error occurs,
// it returns the error.
func (ms *MongoStorage) DeleteUserVerificationCode(user *User, t CodeType) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.delVerificationCode(ctx, user.ID, t)
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
		CreatedAt:  time.Now(),
		Expiration: exp,
		Attempts:   1,
	}
	opts := options.Replace().SetUpsert(true)
	_, err := ms.verifications.ReplaceOne(ctx, filter, verification, opts)
	return err
}

// VerificationCodeCheckAndAddAttempt atomically records one verification attempt for the
// user's code of the given type, but only while the stored attempt count is still below
// maxAttempts. It returns recorded=true when the attempt was counted and recorded=false when
// the cap had already been reached (no increment performed) — a single conditional update, so
// concurrent submissions cannot push the counter past the cap. It is the fail-closed guard on
// the code-guessing path (mirrors IncrementCSPAuthAttempts). It returns ErrNotFound when no
// such code exists.
func (ms *MongoStorage) VerificationCodeCheckAndAddAttempt(user *User, t CodeType, maxAttempts int) (bool, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// conditional increment: only bump attempts while still below the cap
	res, err := ms.verifications.UpdateOne(ctx,
		bson.M{"_id": user.ID, "type": t, "attempts": bson.M{"$lt": maxAttempts}},
		bson.M{"$inc": bson.M{"attempts": 1}})
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	// no document matched: either no code exists or the cap is already reached. Distinguish the
	// two so the caller can return the right error.
	if err := ms.verifications.FindOne(ctx, bson.M{"_id": user.ID, "type": t}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			return false, ErrNotFound
		}
		return false, err
	}
	return false, nil
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
