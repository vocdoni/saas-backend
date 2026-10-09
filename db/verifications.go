package db

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// delVerificationCode private method deletes the verification code for the
// user and type provided. This method must be called with the keysLock held.
func (ms *MongoStorage) delVerificationCode(ctx context.Context, id uint64, t CodeType) error {
	// delete the verification code for the user provided
	_, err := ms.verifications.DeleteOne(ctx, bson.M{"userId": id, "type": t})
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

// ConsumeVerificationCode atomically consumes the user's verification code of the
// given type, matching the exact sealed code. Exactly one document must be deleted;
// when nothing matches (already consumed by a concurrent request, replaced, or never
// issued) it returns ErrNotFound, so a code can never be redeemed twice.
func (ms *MongoStorage) ConsumeVerificationCode(user *User, t CodeType, sealedCode []byte) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.verifications.DeleteOne(ctx, bson.M{"userId": user.ID, "type": t, "sealedCode": sealedCode})
	if err != nil {
		return err
	}
	if res.DeletedCount != 1 {
		return ErrNotFound
	}
	return nil
}

// UserVerificationCode returns the verification code for the user provided. If
// the user has not a verification code, it returns an specific error, if other
// error occurs, it returns the error.
func (ms *MongoStorage) UserVerificationCode(user *User, t CodeType) (*UserVerification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	result := ms.verifications.FindOne(ctx, bson.M{"userId": user.ID, "type": t})
	verification := &UserVerification{}
	if err := result.Decode(verification); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return verification, nil
}

// SetVerificationCode method sets the verification code described by the
// verification provided (user, type, sealed code, expiration and, for email
// updates, the pending email). Codes are stored per (user, type), so a code of
// one type never replaces a pending code of another type. If the user already
// has a code of the same type, it is replaced and its guess/send counters are
// reset: guesses start at zero and the initial delivery counts as one send.
// If an error occurs, it returns the error.
func (ms *MongoStorage) SetVerificationCode(verification *UserVerification) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// try to get the user to ensure it exists
	if _, err := ms.fetchUserFromDB(ctx, verification.UserID); err != nil {
		return err
	}
	// replace (or insert) the verification code for the (user, type) pair
	now := time.Now()
	verification.CreatedAt = now
	verification.LastSentAt = now
	verification.Attempts = 0
	verification.Sends = 1
	filter := bson.M{"userId": verification.UserID, "type": verification.Type}
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
		bson.M{"userId": user.ID, "type": t, "attempts": bson.M{"$lt": maxAttempts}},
		bson.M{"$inc": bson.M{"attempts": 1}})
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	// no document matched: either no code exists or the cap is already reached. Distinguish the
	// two so the caller can return the right error.
	if err := ms.verifications.FindOne(ctx, bson.M{"userId": user.ID, "type": t}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			return false, ErrNotFound
		}
		return false, err
	}
	return false, nil
}

// VerificationCodeTrySend atomically authorizes one more delivery of the user's code of the
// given type: a single conditional update that only matches while the number of sends is still
// below maxSends and the previous delivery is older than cooldown, so concurrent requests can
// neither flood the mailbox nor push the send counter past the cap. The send counter is separate
// from the guess-attempt counter, so resending a code never consumes guess budget. It returns
// sent=true when the delivery was authorized, sent=false when the cap or the cooldown blocked
// it, and ErrNotFound when no such code exists.
func (ms *MongoStorage) VerificationCodeTrySend(user *User, t CodeType, cooldown time.Duration, maxSends int) (bool, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := time.Now()
	res, err := ms.verifications.UpdateOne(ctx,
		bson.M{
			"userId":     user.ID,
			"type":       t,
			"sends":      bson.M{"$lt": maxSends},
			"lastSentAt": bson.M{"$lte": now.Add(-cooldown)},
		},
		bson.M{"$inc": bson.M{"sends": 1}, "$set": bson.M{"lastSentAt": now}})
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	// no document matched: either no code exists or the send budget/cooldown blocked it
	if err := ms.verifications.FindOne(ctx, bson.M{"userId": user.ID, "type": t}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			return false, ErrNotFound
		}
		return false, err
	}
	return false, nil
}
