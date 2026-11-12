package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// AttestationRecord is the attestation the CSP signed an attested voter's ballots against, one per
// voter of a process. It is written before the voter's first signature, keeps the first attestation
// used, and lists the questions (upstream election ids) the CSP signed for. It holds no contact, no
// salt and no voting address.
type AttestationRecord struct {
	UserID      internal.HexBytes   `json:"userId" bson:"_id"`
	ProcessID   bson.ObjectID       `json:"processId" bson:"processId"`
	VoterID     internal.HexBytes   `json:"voterId" bson:"voterId"`
	ContactHash internal.HexBytes   `json:"contactHash,omitempty" bson:"contactHash,omitempty"`
	ExpiresAt   int64               `json:"expiresAt" bson:"expiresAt"`
	Signature   internal.HexBytes   `json:"signature" bson:"signature"`
	Signer      common.Address      `json:"signer" bson:"signer"`
	Questions   []internal.HexBytes `json:"questions" bson:"questions"`
	CreatedAt   time.Time           `json:"createdAt" bson:"createdAt"`
}

// RecordAttestedSignature records that the CSP is about to sign question upstreamID for the attested
// voter userID of processID. The first call for a voter stores the attestation; later calls only add
// the question. It runs before the signature, so no signature exists without its record.
func (ms *MongoStorage) RecordAttestedSignature(processID bson.ObjectID, userID internal.HexBytes,
	attestation *CSPAuthAttestation, upstreamID internal.HexBytes,
) error {
	if processID.IsZero() || len(userID) == 0 || attestation == nil || len(upstreamID) == 0 {
		return ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": userID, "processId": processID}
	update := bson.M{
		"$setOnInsert": bson.M{
			"voterId":     attestation.VoterID,
			"contactHash": attestation.ContactHash,
			"expiresAt":   attestation.ExpiresAt,
			"signature":   attestation.Signature,
			"signer":      attestation.Signer,
			"createdAt":   time.Now(),
		},
		"$addToSet": bson.M{"questions": upstreamID},
	}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := ms.cspAttestations.UpdateOne(ctx, filter, update, opts)
	if mongo.IsDuplicateKeyError(err) {
		// a concurrent first sign inserted the record between our match and insert: add the question
		_, err = ms.cspAttestations.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to record attested signature: %w", err)
	}
	return nil
}

// AttestationRecord returns the attestation record of the attested voter userID of processID, or
// ErrNotFound.
func (ms *MongoStorage) AttestationRecord(processID bson.ObjectID, userID internal.HexBytes) (*AttestationRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	record := &AttestationRecord{}
	err := ms.cspAttestations.FindOne(ctx, bson.M{"_id": userID, "processId": processID}).Decode(record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get attestation record: %w", err)
	}
	return record, nil
}

// AttestationRecords returns a page of the attestation records of processID, ordered by user id, and
// their total count.
func (ms *MongoStorage) AttestationRecords(processID bson.ObjectID, page, limit int64) (int64, []AttestationRecord, error) {
	findOptions := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}})
	return paginatedDocuments[AttestationRecord](ms.cspAttestations, page, limit,
		bson.M{"processId": processID}, findOptions)
}

// CountAttestationRecords returns how many attested voters of processID the CSP signed for.
func (ms *MongoStorage) CountAttestationRecords(processID bson.ObjectID) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	n, err := ms.cspAttestations.CountDocuments(ctx, bson.M{"processId": processID})
	if err != nil {
		return 0, fmt.Errorf("failed to count attestation records: %w", err)
	}
	return n, nil
}

// RotateAttestationSigner makes signer the active signer of the attested census censusID and appends
// it to the signer history. Rotating to the active signer is a no-op. It returns ErrNotFound when
// censusID is not an attested census.
func (ms *MongoStorage) RotateAttestationSigner(censusID bson.ObjectID, signer common.Address) error {
	if signer == (common.Address{}) {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := time.Now()
	res, err := ms.censuses.UpdateOne(ctx,
		bson.M{"_id": censusID, "attested": bson.M{"$type": "object"}, "attested.signer": bson.M{"$ne": signer}},
		bson.M{
			"$set":  bson.M{"attested.signer": signer, "updatedAt": now},
			"$push": bson.M{"attested.signerHistory": AttestationSigner{Signer: signer, From: now}},
		})
	if err != nil {
		return fmt.Errorf("failed to rotate attestation signer: %w", err)
	}
	if res.MatchedCount == 1 {
		return nil
	}
	n, err := ms.censuses.CountDocuments(ctx, bson.M{"_id": censusID, "attested": bson.M{"$type": "object"}})
	if err != nil {
		return fmt.Errorf("failed to rotate attestation signer: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GrowAttestedCensusSize raises the declared size of the attested census censusID to size. Setting
// the current size is a no-op; a smaller size returns ErrInvalidData and a census that is not
// attested ErrNotFound.
func (ms *MongoStorage) GrowAttestedCensusSize(censusID bson.ObjectID, size int64) error {
	if size <= 0 {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	attested := bson.M{"_id": censusID, "attested": bson.M{"$type": "object"}}
	res, err := ms.censuses.UpdateOne(ctx,
		bson.M{"_id": censusID, "attested": bson.M{"$type": "object"}, "size": bson.M{"$lte": size}},
		bson.M{"$set": bson.M{"size": size, "updatedAt": time.Now()}})
	if err != nil {
		return fmt.Errorf("failed to grow census size: %w", err)
	}
	if res.MatchedCount == 1 {
		return nil
	}
	n, err := ms.censuses.CountDocuments(ctx, attested)
	if err != nil {
		return fmt.Errorf("failed to grow census size: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return ErrInvalidData
}
