package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Daily CSP counter kinds. Each kind namespaces its keys, so the same key bytes
// never collide across kinds.
const (
	// CSPCounterChallengeSends counts the OTP challenges sent (new tokens + resends) for one
	// member on one voting process, per UTC day.
	CSPCounterChallengeSends = "challengeSends"
	// CSPCounterAuthFailureMember counts failed step-0 authentications for one identified census
	// participant on one voting process, per UTC day.
	CSPCounterAuthFailureMember = "authFailuresMember"
	// CSPCounterAuthFailureProcess counts failed step-0 authentications across one whole voting
	// process, per UTC day.
	CSPCounterAuthFailureProcess = "authFailuresProcess"
)

// cspCounter is one daily counter document. The _id derives from (kind, key, UTC day), so every
// counter automatically rolls over at midnight UTC; ExpiresAt backs a TTL index that garbage
// collects stale days.
type cspCounter struct {
	ID        internal.HexBytes `bson:"_id"`
	Kind      string            `bson:"kind"`
	Count     int               `bson:"count"`
	ExpiresAt time.Time         `bson:"expiresat"`
}

// cspCounterID derives the daily counter document id from the kind, the caller's key bytes and the
// current UTC day.
func cspCounterID(kind string, key []byte, day string) internal.HexBytes {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(key)
	h.Write([]byte{0})
	h.Write([]byte(day))
	return h.Sum(nil)
}

// cspCounterToday returns the id of today's counter for (kind, key) and the document expiry (two
// days out, so a counter never disappears mid-day under TTL-monitor lag).
func cspCounterToday(kind string, key []byte) (internal.HexBytes, time.Time) {
	now := time.Now().UTC()
	return cspCounterID(kind, key, now.Format(time.DateOnly)), now.Add(48 * time.Hour)
}

// ClaimCSPCounter atomically increments today's counter for (kind, key), refusing once max
// increments have been recorded in the current UTC day. It returns whether the increment was
// granted. A non-positive max disables the counter (always granted, nothing written).
func (ms *MongoStorage) ClaimCSPCounter(kind string, key []byte, maxCount int) (bool, error) {
	if kind == "" || len(key) == 0 {
		return false, ErrBadInputs
	}
	if maxCount <= 0 {
		return true, nil
	}
	id, expiry := cspCounterToday(kind, key)
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// conditional increment: the filter only matches while the cap is not reached, and the upsert
	// creates the day's document on first use. Once count == max the filter matches nothing and,
	// because it pins _id, the upsert collides with the unique _id — a duplicate-key error means
	// the cap is reached.
	filter := bson.M{"_id": id, "count": bson.M{"$lt": maxCount}}
	update := bson.M{
		"$inc":         bson.M{"count": 1},
		"$setOnInsert": bson.M{"kind": kind, "expiresat": expiry},
	}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := ms.cspCounters.UpdateOne(ctx, filter, update, opts); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to claim csp counter: %w", err)
	}
	return true, nil
}

// CSPCounterReached reports whether today's counter for (kind, key) has reached max, without
// incrementing it. A non-positive max disables the counter (never reached).
func (ms *MongoStorage) CSPCounterReached(kind string, key []byte, maxCount int) (bool, error) {
	if kind == "" || len(key) == 0 {
		return false, ErrBadInputs
	}
	if maxCount <= 0 {
		return false, nil
	}
	id, _ := cspCounterToday(kind, key)
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	counter := new(cspCounter)
	err := ms.cspCounters.FindOne(ctx, bson.M{"_id": id}).Decode(counter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		return false, fmt.Errorf("failed to read csp counter: %w", err)
	}
	return counter.Count >= maxCount, nil
}
