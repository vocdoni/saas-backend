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

// CSP counter kinds. Each kind namespaces its keys, so the same key bytes never collide across
// kinds, and sets the length of the window its counters reset after (see CSPCounterWindow).
const (
	// CSPCounterChallengeSends counts the OTP challenges sent (new tokens + resends) for one
	// member on one voting process, per UTC day.
	CSPCounterChallengeSends = "challengeSends"
	// CSPCounterAuthFailures counts failed step-0 authentications for one submission (the login
	// and contact fields a request submits, whether or not they match a census participant) on one
	// voting process, per UTC hour.
	CSPCounterAuthFailures = "authFailures"
	// CSPCounterVoterAuthFailures counts failed step-0 authentications for one claimed voter (the
	// login fields a request submits, without the contact) on one voting process, per UTC hour. It
	// is the coarser bound on guessing one voter's contact with ever-changing values.
	CSPCounterVoterAuthFailures = "voterAuthFailures"
	// CSPCounterCertAuthCreates counts certificate sign-in attempts created from one client IP on
	// one voting process, per UTC minute.
	CSPCounterCertAuthCreates = "certAuthCreates"
	// CSPCounterCertAuthFailures counts certificate redeems on one voting process, per UTC hour,
	// under two kinds of key: the presented certificate (before verification) and the verified
	// document number (after it). Only a verified identity reaches a document key, so a forged
	// certificate naming a voter's document spends its own budget, never the voter's.
	CSPCounterCertAuthFailures = "certAuthFailures"
)

// CSPCounterWindow returns the length of the window a counter kind resets after. Failed
// authentications use short hourly windows: anyone who knows a voter's login fields can spend
// that voter's coarse budget, and an hour bounds such a lockout while still capping contact
// guessing at 24 windows a day. Challenge sends cost money per unit, so their budget is daily.
func CSPCounterWindow(kind string) time.Duration {
	switch kind {
	case CSPCounterAuthFailures, CSPCounterVoterAuthFailures, CSPCounterCertAuthFailures:
		return time.Hour
	case CSPCounterCertAuthCreates:
		return time.Minute
	default:
		return 24 * time.Hour
	}
}

// cspCounter is one counter document. The _id derives from (kind, key, window start), so every
// counter rolls over when its window ends; ExpiresAt backs a TTL index that garbage collects
// stale windows.
type cspCounter struct {
	ID        internal.HexBytes `bson:"_id"`
	Kind      string            `bson:"kind"`
	Count     int               `bson:"count"`
	ExpiresAt time.Time         `bson:"expiresat"`
}

// cspCounterID derives the counter document id from the kind, the caller's key bytes and the
// label of the window.
func cspCounterID(kind string, key []byte, window string) internal.HexBytes {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(key)
	h.Write([]byte{0})
	h.Write([]byte(window))
	return h.Sum(nil)
}

// cspCounterBucket is the counter document of (kind, key) for the window containing a moment.
type cspCounterBucket struct {
	id internal.HexBytes
	// resetAt is when the window ends, and a refused claim can next be granted.
	resetAt time.Time
	// expiry is two windows out, so a counter never disappears mid-window under TTL-monitor lag.
	expiry time.Time
}

// currentCSPCounterBucket returns the bucket of (kind, key) for the current window. Windows are
// aligned to UTC (time.Truncate counts from the zero time, a UTC midnight), so the daily ones
// reset at midnight UTC and the hourly ones on the hour.
func currentCSPCounterBucket(kind string, key []byte, now time.Time) cspCounterBucket {
	window := CSPCounterWindow(kind)
	start := now.UTC().Truncate(window)
	return cspCounterBucket{
		id:      cspCounterID(kind, key, start.Format(time.RFC3339)),
		resetAt: start.Add(window),
		expiry:  start.Add(2 * window),
	}
}

// CSPCounterClaim identifies the counter document a granted claim incremented, so the unit can be
// given back with ReleaseCSPCounter to that same window's counter, even if the window rolled over
// in between. It is empty when the counter was disabled and nothing was written.
type CSPCounterClaim internal.HexBytes

// CSPCounterResult is the outcome of ClaimCSPCounter.
type CSPCounterResult struct {
	// Granted reports whether the unit was granted.
	Granted bool
	// Claim is what ReleaseCSPCounter takes to give a granted unit back.
	Claim CSPCounterClaim
	// ResetAt is when the current window ends; zero when the counter is disabled.
	ResetAt time.Time
}

// ClaimCSPCounter atomically increments the current window's counter for (kind, key), refusing
// once max increments have been recorded in that window. A non-positive max disables the counter
// (always granted, nothing written, empty claim).
func (ms *MongoStorage) ClaimCSPCounter(kind string, key []byte, maxCount int) (CSPCounterResult, error) {
	if kind == "" || len(key) == 0 {
		return CSPCounterResult{}, ErrBadInputs
	}
	if maxCount <= 0 {
		return CSPCounterResult{Granted: true}, nil
	}
	bucket := currentCSPCounterBucket(kind, key, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// conditional increment: the filter only matches while the cap is not reached, and the upsert
	// creates the window's document on first use. Once count == max the filter matches nothing
	// and, because it pins _id, the upsert collides with the unique _id — a duplicate-key error
	// means the cap is reached. The update is atomic on the server, so no in-process lock is taken.
	filter := bson.M{"_id": bucket.id, "count": bson.M{"$lt": maxCount}}
	update := bson.M{
		"$inc":         bson.M{"count": 1},
		"$setOnInsert": bson.M{"kind": kind, "expiresat": bucket.expiry},
	}
	opts := options.UpdateOne().SetUpsert(true)
	// A duplicate key is also what the loser of two concurrent first claims of the window gets:
	// the server only retries such an upsert itself when the filter is an equality on _id alone.
	// The document exists by then, so one retry tells the two cases apart: it either increments it
	// or collides again because the cap is really reached.
	for attempt := 0; ; attempt++ {
		_, err := ms.cspCounters.UpdateOne(ctx, filter, update, opts)
		if err == nil {
			return CSPCounterResult{Granted: true, Claim: CSPCounterClaim(bucket.id), ResetAt: bucket.resetAt}, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return CSPCounterResult{}, fmt.Errorf("failed to claim csp counter: %w", err)
		}
		if attempt > 0 {
			return CSPCounterResult{ResetAt: bucket.resetAt}, nil
		}
	}
}

// ReleaseCSPCounter gives back the unit a granted ClaimCSPCounter took. It never takes the counter
// below zero, and an empty claim (the counter was disabled) is a no-op.
func (ms *MongoStorage) ReleaseCSPCounter(claim CSPCounterClaim) error {
	if len(claim) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": internal.HexBytes(claim), "count": bson.M{"$gt": 0}}
	if _, err := ms.cspCounters.UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"count": -1}}); err != nil {
		return fmt.Errorf("failed to release csp counter: %w", err)
	}
	return nil
}

// CSPCounterReached reports whether the current window's counter for (kind, key) has reached max,
// without incrementing it. A non-positive max disables the counter (never reached).
func (ms *MongoStorage) CSPCounterReached(kind string, key []byte, maxCount int) (bool, error) {
	if kind == "" || len(key) == 0 {
		return false, ErrBadInputs
	}
	if maxCount <= 0 {
		return false, nil
	}
	bucket := currentCSPCounterBucket(kind, key, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	counter := new(cspCounter)
	err := ms.cspCounters.FindOne(ctx, bson.M{"_id": bucket.id}).Decode(counter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return false, nil
		}
		return false, fmt.Errorf("failed to read csp counter: %w", err)
	}
	return counter.Count >= maxCount, nil
}
