package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DefaultBlindSessionTTL is how long a prepared blind-signature session stays
// usable. A voter only needs it long enough to blind the ballot bundle and come
// back, so this is deliberately short: an abandoned session is dead weight, and
// the shorter it lives the smaller the window in which a stolen session is worth
// anything.
const DefaultBlindSessionTTL = 10 * time.Minute

// CSPBlindSession is an in-flight blind-signature session: the ephemeral secret
// k generated when a voter prepares an anonymous signature, held until the
// matching sign call consumes it.
//
// The session is single-use by construction — ConsumeCSPBlindSession deletes and
// returns it in one atomic operation. That matters more than it looks: signing
// two different blinded messages with the same k leaks the salted private key,
// because d = (s1 - s2) / (m1 - m2). A load-then-delete would let two concurrent
// sign requests both read the same k.
//
// It is stored rather than kept in memory because prepare and sign are separate
// requests that need not land on the same replica.
type CSPBlindSession struct {
	// ID is hash(userID + electionID): one live session per voter per election.
	ID         internal.HexBytes `json:"id" bson:"_id"`
	UserID     internal.HexBytes `json:"userID" bson:"userid"`
	ElectionID internal.HexBytes `json:"electionID" bson:"electionid"`
	// SecretK is the ephemeral scalar k, 32 bytes big-endian. It must never
	// leave the server, so it is excluded from JSON serialization.
	SecretK   []byte    `json:"-" bson:"secretk"`
	CreatedAt time.Time `json:"createdAt" bson:"createdat"`
	// ExpiresAt drives the TTL index. Mongo's TTL monitor only sweeps about once
	// a minute, so reads also filter on it rather than trusting the sweep.
	ExpiresAt time.Time `json:"expiresAt" bson:"expiresat"`
}

// cspBlindSessionID derives the session key from the user and election ids.
//
// It builds a fresh buffer rather than append(userID, electionID...): userID is
// a slice owned by the caller, and appending to it can write into its spare
// capacity, silently corrupting the caller's copy.
func cspBlindSessionID(userID, electionID internal.HexBytes) internal.HexBytes {
	buf := make([]byte, 0, len(userID)+len(electionID))
	buf = append(buf, userID...)
	buf = append(buf, electionID...)
	sum := sha256.Sum256(buf)
	return sum[:]
}

// SetCSPBlindSession stores the ephemeral secret k for a voter's blind-signature
// session, replacing any session that voter already had open on the same
// election. Replacing is intentional: preparing again hands the client a fresh R
// point, and the previous one must stop being signable.
func (ms *MongoStorage) SetCSPBlindSession(userID, electionID internal.HexBytes, secretK []byte, ttl time.Duration) error {
	if userID == nil || electionID == nil || len(secretK) == 0 {
		return ErrBadInputs
	}
	if ttl <= 0 {
		ttl = DefaultBlindSessionTTL
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	now := time.Now()
	session := CSPBlindSession{
		ID:         cspBlindSessionID(userID, electionID),
		UserID:     userID,
		ElectionID: electionID,
		SecretK:    secretK,
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}
	opts := options.Replace().SetUpsert(true)
	if _, err := ms.cspBlindSessions.ReplaceOne(ctx, bson.M{"_id": session.ID}, session, opts); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// ConsumeCSPBlindSession atomically fetches and deletes the blind-signature
// session for the given voter and election, returning the secret k exactly once.
// A second call for the same session returns ErrCSPBlindSessionNotFound, which
// is what makes k single-use and keeps the salted private key safe.
//
// Expired sessions are treated as absent even if the TTL sweep has not reached
// them yet.
func (ms *MongoStorage) ConsumeCSPBlindSession(userID, electionID internal.HexBytes) (*CSPBlindSession, error) {
	if userID == nil || electionID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"_id":       cspBlindSessionID(userID, electionID),
		"expiresat": bson.M{"$gt": time.Now()},
	}
	session := new(CSPBlindSession)
	if err := ms.cspBlindSessions.FindOneAndDelete(ctx, filter).Decode(session); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrCSPBlindSessionNotFound
		}
		return nil, err
	}
	return session, nil
}

// DeleteCSPBlindSessionsByElection removes every blind-signature session opened
// for an election. It is best-effort cleanup for teardown paths; the TTL index
// would expire these anyway.
func (ms *MongoStorage) DeleteCSPBlindSessionsByElection(electionID internal.HexBytes) (int64, error) {
	if electionID == nil {
		return 0, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	res, err := ms.cspBlindSessions.DeleteMany(ctx, bson.M{"electionid": electionID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}
