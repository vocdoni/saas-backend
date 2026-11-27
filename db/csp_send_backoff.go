package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CSPSendWindow is the rolling window the send ceiling counts sends over.
const CSPSendWindow = 24 * time.Hour

// cspSendBackoffFreeLevels is the backoff level up to which a send waits only for the
// per-destination cooldown; each level past it doubles the voter-level wait.
const cspSendBackoffFreeLevels = 3

// cspSendBackoffMaxShift bounds the doubling exponent, so the wait never overflows before it is
// capped at the maximum wait.
const cspSendBackoffMaxShift = 30

// CSPSendBackoff is the challenge-send backoff of one member on one voting process.
type CSPSendBackoff struct {
	ID internal.HexBytes `bson:"_id"`
	// Sends are the send times of the last CSPSendWindow, oldest first. Each is unique, so a
	// release removes exactly its own send.
	Sends []time.Time `bson:"sends"`
	// Level counts the sends since the last reset.
	Level int `bson:"level"`
	// Failures counts the failed step-1 attempts since the last reset.
	Failures int `bson:"failures"`
	// LastSend is the latest send time, which the voter-level wait runs from.
	LastSend time.Time `bson:"lastsend,omitempty"`
	// ResetAt is when Level and Failures were last reset: only a send at or after it counts in
	// Level, so releasing an older one leaves Level alone.
	ResetAt time.Time `bson:"resetat,omitempty"`
	// ExpiresAt backs the TTL index; it is a CSPSendWindow past the latest send.
	ExpiresAt time.Time `bson:"expiresat"`
}

// CSPSendBackoffPolicy sets how the send backoff grows and where it stops.
type CSPSendBackoffPolicy struct {
	// Ceiling caps the sends per rolling CSPSendWindow; non-positive disables it.
	Ceiling int
	// BaseWait is the wait the doubling starts from (the notification cooldown).
	BaseWait time.Duration
	// MaxWait caps the voter-level wait; non-positive disables the wait.
	MaxWait time.Duration
	// QuietPeriod is the time without sends after which the next send resets Level and Failures.
	QuietPeriod time.Duration
	// FailuresPerLevel is how many failed attempts add one level (the attempts a code takes).
	FailuresPerLevel int
}

// wait is the voter-level wait after a send at backoff level k: none up to
// cspSendBackoffFreeLevels, then BaseWait doubled per level past it, capped at MaxWait.
func (p CSPSendBackoffPolicy) wait(k int) time.Duration {
	if k <= cspSendBackoffFreeLevels || p.MaxWait <= 0 {
		return 0
	}
	// whole milliseconds, as the server computes it; capped before shifting, so it never overflows
	base, maxWait := p.BaseWait.Truncate(time.Millisecond), p.MaxWait.Truncate(time.Millisecond)
	shift := min(k-cspSendBackoffFreeLevels, cspSendBackoffMaxShift)
	if base > maxWait>>shift {
		return maxWait
	}
	return max(base<<shift, 0)
}

// level is the backoff level a send state gives: its sends plus a level per FailuresPerLevel
// failed attempts.
func (p CSPSendBackoffPolicy) level(sends, failures int) int {
	if p.FailuresPerLevel <= 0 {
		return sends
	}
	return sends + failures/p.FailuresPerLevel
}

// CSPSendRefusal is why the send backoff refuses a send at some moment; the zero value refuses
// nothing.
type CSPSendRefusal struct {
	// CeilingFreeAt is when the oldest send of the window ages out, set when the ceiling is reached.
	CeilingFreeAt time.Time
	// WaitUntil is when the voter-level wait ends, set while it runs.
	WaitUntil time.Time
}

// Refused reports whether the send is refused.
func (r CSPSendRefusal) Refused() bool {
	return !r.CeilingFreeAt.IsZero() || !r.WaitUntil.IsZero()
}

// Refusal evaluates, at now, what a send claim would refuse, with the same rules ClaimCSPSend
// applies on the server. A nil backoff (no sends yet) refuses nothing.
func (b *CSPSendBackoff) Refusal(p CSPSendBackoffPolicy, now time.Time) CSPSendRefusal {
	var r CSPSendRefusal
	if b == nil {
		return r
	}
	cutoff := now.Add(-CSPSendWindow)
	var live []time.Time
	for _, s := range b.Sends {
		if s.After(cutoff) {
			live = append(live, s)
		}
	}
	if p.Ceiling > 0 && len(live) >= p.Ceiling {
		r.CeilingFreeAt = slices.MinFunc(live, func(a, b time.Time) int { return a.Compare(b) }).Add(CSPSendWindow)
	}
	if b.LastSend.IsZero() || !b.LastSend.After(now.Add(-p.QuietPeriod)) {
		return r
	}
	// no wait refuses nothing, even when LastSend was moved a millisecond ahead to stay unique
	wait := p.wait(p.level(b.Level, b.Failures))
	if until := b.LastSend.Add(wait); wait > 0 && until.After(now) {
		r.WaitUntil = until
	}
	return r
}

// CSPSendBackoffClaim identifies a send ClaimCSPSend granted, for ReleaseCSPSend. The zero value
// holds nothing.
type CSPSendBackoffClaim struct {
	ID internal.HexBytes
	// At is the unique send time the claim stored.
	At time.Time
}

// cspSendBackoffID derives the backoff document id from the caller's key (member and process).
func cspSendBackoffID(key []byte) internal.HexBytes {
	sum := sha256.Sum256(key)
	return sum[:]
}

// CSPSendBackoff returns the send backoff stored under key, nil when there is none.
func (ms *MongoStorage) CSPSendBackoff(key []byte) (*CSPSendBackoff, error) {
	if len(key) == 0 {
		return nil, ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	b := new(CSPSendBackoff)
	err := ms.cspSendBackoff.FindOne(ctx, bson.M{"_id": cspSendBackoffID(key)}).Decode(b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read csp send backoff: %w", err)
	}
	return b, nil
}

// cspSendBackoffAt is the send time a claim stores: now, or a millisecond past the latest send when
// that is not earlier, so every stored send time is unique.
func cspSendBackoffAt(now time.Time) bson.M {
	return bson.M{"$max": bson.A{now, bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$lastsend", time.Time{}}}, 1}}}}
}

// ClaimCSPSend claims a send in the backoff stored under key, in one conditional update: it drops
// the sends older than CSPSendWindow and, unless the ceiling is reached or the voter-level wait
// since the latest send still runs, stores the send, resetting Level and Failures first after a
// quiet period. When it refuses it writes nothing and returns why, read back from the document. A
// granted claim is given back with ReleaseCSPSend.
func (ms *MongoStorage) ClaimCSPSend(key []byte, p CSPSendBackoffPolicy) (CSPSendBackoffClaim, CSPSendRefusal, error) {
	if len(key) == 0 {
		return CSPSendBackoffClaim{}, CSPSendRefusal{}, ErrBadInputs
	}
	id := cspSendBackoffID(key)
	// a refused claim re-reads the document to say why; if a concurrent release freed the send in
	// between, nothing refuses it any more and it claims again
	for range 3 {
		now := cspNow()
		claim, claimed, err := ms.claimCSPSendOnce(id, p, now)
		if err != nil {
			return CSPSendBackoffClaim{}, CSPSendRefusal{}, err
		}
		if claimed {
			return claim, CSPSendRefusal{}, nil
		}
		b, err := ms.CSPSendBackoff(key)
		if err != nil {
			return CSPSendBackoffClaim{}, CSPSendRefusal{}, err
		}
		if refusal := b.Refusal(p, now); refusal.Refused() {
			return CSPSendBackoffClaim{}, refusal, nil
		}
	}
	return CSPSendBackoffClaim{}, CSPSendRefusal{}, fmt.Errorf("csp send claim lost to concurrent updates")
}

// claimCSPSendOnce makes sure the backoff document exists and runs the conditional claim once.
func (ms *MongoStorage) claimCSPSendOnce(id internal.HexBytes, p CSPSendBackoffPolicy, now time.Time,
) (CSPSendBackoffClaim, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// an upsert filtering on _id alone, so the server retries a duplicate key between concurrent
	// first claims itself; the claim below then only ever updates
	if _, err := ms.cspSendBackoff.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$setOnInsert": bson.M{"expiresat": now.Add(CSPSendWindow)}},
		options.UpdateOne().SetUpsert(true)); err != nil {
		return CSPSendBackoffClaim{}, false, fmt.Errorf("failed to create csp send backoff: %w", err)
	}

	lastSend := bson.M{"$ifNull": bson.A{"$lastsend", time.Time{}}}
	quiet := bson.M{"$lte": bson.A{lastSend, now.Add(-p.QuietPeriod)}}
	live := bson.M{"$filter": bson.M{
		"input": bson.M{"$ifNull": bson.A{"$sends", bson.A{}}},
		"as":    "s",
		"cond":  bson.M{"$gt": bson.A{"$$s", now.Add(-CSPSendWindow)}},
	}}
	// no wait refuses nothing, even when lastsend was moved a millisecond ahead to stay unique
	wait := cspSendBackoffWaitExpr(p, quiet)
	conds := bson.A{bson.M{"$or": bson.A{
		bson.M{"$eq": bson.A{wait, 0}},
		bson.M{"$lte": bson.A{bson.M{"$add": bson.A{lastSend, wait}}, now}},
	}}}
	if p.Ceiling > 0 {
		conds = append(conds, bson.M{"$lt": bson.A{bson.M{"$size": live}, p.Ceiling}})
	}
	at := cspSendBackoffAt(now)
	pipeline := mongo.Pipeline{
		{{Key: "$set", Value: bson.M{
			"sends":    bson.M{"$concatArrays": bson.A{live, bson.A{at}}},
			"level":    bson.M{"$add": bson.A{bson.M{"$cond": bson.A{quiet, 0, bson.M{"$ifNull": bson.A{"$level", 0}}}}, 1}},
			"failures": bson.M{"$cond": bson.A{quiet, 0, bson.M{"$ifNull": bson.A{"$failures", 0}}}},
			"resetat":  bson.M{"$cond": bson.A{quiet, at, "$resetat"}},
			"lastsend": at,
		}}},
		{{Key: "$set", Value: bson.M{"expiresat": bson.M{"$add": bson.A{"$lastsend", CSPSendWindow.Milliseconds()}}}}},
	}
	var after struct {
		LastSend time.Time `bson:"lastsend"`
	}
	err := ms.cspSendBackoff.FindOneAndUpdate(ctx,
		bson.M{"_id": id, "$expr": bson.M{"$and": conds}}, pipeline,
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(bson.M{"lastsend": 1}),
	).Decode(&after)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return CSPSendBackoffClaim{}, false, nil
	}
	if err != nil {
		return CSPSendBackoffClaim{}, false, fmt.Errorf("failed to claim csp send: %w", err)
	}
	return CSPSendBackoffClaim{ID: id, At: after.LastSend}, true, nil
}

// cspSendBackoffWaitExpr is the aggregation expression of the voter-level wait in milliseconds
// (CSPSendBackoffPolicy.wait at the document's level), zero after a quiet period.
func cspSendBackoffWaitExpr(p CSPSendBackoffPolicy, quiet bson.M) bson.M {
	if p.MaxWait <= 0 {
		return bson.M{"$literal": 0}
	}
	k := bson.M{"$ifNull": bson.A{"$level", 0}}
	if p.FailuresPerLevel > 0 {
		k = bson.M{"$add": bson.A{k, bson.M{"$floor": bson.M{"$divide": bson.A{
			bson.M{"$ifNull": bson.A{"$failures", 0}}, p.FailuresPerLevel,
		}}}}}
	}
	shift := bson.M{"$min": bson.A{bson.M{"$subtract": bson.A{k, cspSendBackoffFreeLevels}}, cspSendBackoffMaxShift}}
	wait := bson.M{"$min": bson.A{
		bson.M{"$multiply": bson.A{p.BaseWait.Milliseconds(), bson.M{"$pow": bson.A{2, shift}}}},
		p.MaxWait.Milliseconds(),
	}}
	return bson.M{"$toLong": bson.M{"$cond": bson.A{
		bson.M{"$or": bson.A{quiet, bson.M{"$lte": bson.A{k, cspSendBackoffFreeLevels}}}}, 0, wait,
	}}}
}

// ReleaseCSPSend gives back a send ClaimCSPSend granted, for one that never reached a provider: it
// removes the claim's own send time, lowers Level when the send counted in it, and moves LastSend
// back to the latest remaining send. It only matches while the document holds that send time, so it
// never undoes another send and gives a claim back at most once. A zero claim is a no-op.
func (ms *MongoStorage) ReleaseCSPSend(claim CSPSendBackoffClaim) error {
	if len(claim.ID) == 0 || claim.At.IsZero() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	remaining := bson.M{"$filter": bson.M{
		"input": "$sends",
		"as":    "s",
		"cond":  bson.M{"$ne": bson.A{"$$s", claim.At}},
	}}
	counted := bson.M{"$and": bson.A{
		bson.M{"$gte": bson.A{claim.At, bson.M{"$ifNull": bson.A{"$resetat", time.Time{}}}}},
		bson.M{"$gt": bson.A{"$level", 0}},
	}}
	pipeline := mongo.Pipeline{
		{{Key: "$set", Value: bson.M{
			"sends": remaining,
			"level": bson.M{"$cond": bson.A{counted, bson.M{"$subtract": bson.A{"$level", 1}}, "$level"}},
		}}},
		{{Key: "$set", Value: bson.M{"lastsend": bson.M{"$ifNull": bson.A{bson.M{"$max": "$sends"}, "$$REMOVE"}}}}},
	}
	if _, err := ms.cspSendBackoff.UpdateOne(ctx, bson.M{"_id": claim.ID, "sends": claim.At}, pipeline); err != nil {
		return fmt.Errorf("failed to release csp send: %w", err)
	}
	return nil
}

// AddCSPSendFailure records a failed step-1 attempt in the backoff stored under key, so the next
// sends wait longer and a new code hands out no fresh free guesses.
func (ms *MongoStorage) AddCSPSendFailure(key []byte) error {
	if len(key) == 0 {
		return ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspSendBackoff.UpdateOne(ctx, bson.M{"_id": cspSendBackoffID(key)},
		bson.M{"$inc": bson.M{"failures": 1}, "$setOnInsert": bson.M{"expiresat": cspNow().Add(CSPSendWindow)}},
		options.UpdateOne().SetUpsert(true)); err != nil {
		return fmt.Errorf("failed to record csp send failure: %w", err)
	}
	return nil
}

// ResetCSPSendBackoff resets Level and Failures of the backoff stored under key, on a successful
// verification. The sends of the window stay: only aging removes them.
func (ms *MongoStorage) ResetCSPSendBackoff(key []byte) error {
	if len(key) == 0 {
		return ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// past every stored send, so releasing one of them never lowers the fresh level
	if _, err := ms.cspSendBackoff.UpdateOne(ctx, bson.M{"_id": cspSendBackoffID(key)}, mongo.Pipeline{
		{{Key: "$set", Value: bson.M{
			"level": bson.M{"$literal": 0}, "failures": bson.M{"$literal": 0}, "resetat": cspSendBackoffAt(cspNow()),
		}}},
	}); err != nil {
		return fmt.Errorf("failed to reset csp send backoff: %w", err)
	}
	return nil
}
