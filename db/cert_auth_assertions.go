package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CertAuthAssertion is a certificate gateway's proof that a voter holds a certificate for a census
// participant's national ID, redeemable once at step 0 of that process with the code it was issued
// under and the PKCE verifier of CodeChallenge. It stores no document number. A TTL index on
// ExpiresAt removes it.
type CertAuthAssertion struct {
	ID            bson.ObjectID `bson:"_id"`
	CodeHash      []byte        `bson:"codeHash"` // sha256 of the code; the code itself is never stored
	ProcessID     bson.ObjectID `bson:"processId"`
	CensusID      string        `bson:"censusId"`
	ParticipantID string        `bson:"participantId"`
	CodeChallenge string        `bson:"codeChallenge"` // base64url(sha256(codeVerifier))
	Provider      string        `bson:"provider"`
	CertSHA256    string        `bson:"certSha256"`
	CreatedAt     time.Time     `bson:"createdAt"`
	ExpiresAt     time.Time     `bson:"expiresAt"`
	UsedAt        *time.Time    `bson:"usedAt,omitempty"`
}

// CreateCertAuthAssertion stores a new assertion.
func (ms *MongoStorage) CreateCertAuthAssertion(a *CertAuthAssertion) error {
	if len(a.CodeHash) == 0 || a.ProcessID.IsZero() || a.ParticipantID == "" {
		return ErrInvalidData
	}
	if a.ID.IsZero() {
		a.ID = bson.NewObjectID()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.certAuthAssertions.InsertOne(ctx, a); err != nil {
		return fmt.Errorf("failed to store cert auth assertion: %w", err)
	}
	return nil
}

// ConsumeCertAuthAssertion atomically marks as used, and returns, the unused and unexpired
// assertion of the process with the given code hash. Two concurrent calls never both get it. It
// returns ErrNotFound for a code that is unknown, used, expired or of another process.
func (ms *MongoStorage) ConsumeCertAuthAssertion(codeHash []byte, processID bson.ObjectID, now time.Time) (
	*CertAuthAssertion, error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"codeHash":  codeHash,
		"processId": processID,
		"usedAt":    nil,
		"expiresAt": bson.M{"$gt": now},
	}
	update := bson.M{"$set": bson.M{"usedAt": now}}
	a := &CertAuthAssertion{}
	err := ms.certAuthAssertions.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to consume cert auth assertion: %w", err)
	}
	return a, nil
}
