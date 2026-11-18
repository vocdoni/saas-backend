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

// attestedVotersCounterID is the counters document holding how many attested voters of processID
// have a record, the count RecordAttestedSignature caps at the census size.
func attestedVotersCounterID(processID bson.ObjectID) string {
	return "attestedVoters:" + processID.Hex()
}

// RecordAttestedSignature records that the CSP is about to sign question upstreamID for the attested
// voter userID of processID. The first call for a voter stores the attestation; later calls only add
// the question. It runs before the signature, so no signature exists without its record. A voter
// without a record yet is refused with ErrAttestedCensusFull once maxVoters voters have one.
func (ms *MongoStorage) RecordAttestedSignature(processID bson.ObjectID, userID internal.HexBytes,
	attestation *CSPAuthAttestation, upstreamID internal.HexBytes, maxVoters int64,
) error {
	if processID.IsZero() || len(userID) == 0 || attestation == nil || len(upstreamID) == 0 || maxVoters <= 0 {
		return ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": userID, "processId": processID}
	addQuestion := bson.M{"$addToSet": bson.M{"questions": upstreamID}}
	res, err := ms.cspAttestations.UpdateOne(ctx, filter, addQuestion)
	if err != nil {
		return fmt.Errorf("failed to record attested signature: %w", err)
	}
	if res.MatchedCount == 1 {
		return nil
	}
	granted, err := ms.claimAttestedVoter(ctx, processID, maxVoters)
	if err != nil {
		return err
	}
	if !granted {
		return ms.addQuestionOrRefuse(ctx, filter, addQuestion)
	}
	_, err = ms.cspAttestations.InsertOne(ctx, &AttestationRecord{
		UserID:      userID,
		ProcessID:   processID,
		VoterID:     attestation.VoterID,
		ContactHash: attestation.ContactHash,
		ExpiresAt:   attestation.ExpiresAt,
		Signature:   attestation.Signature,
		Signer:      attestation.Signer,
		Questions:   []internal.HexBytes{upstreamID},
		CreatedAt:   time.Now(),
	})
	if err == nil {
		return nil
	}
	// the slot goes back whatever failed: on a duplicate key a concurrent first sign of the same
	// voter holds its own slot and wrote the record, which only needs the question added
	if rErr := ms.releaseAttestedVoter(ctx, processID); rErr != nil {
		return errors.Join(fmt.Errorf("failed to record attested signature: %w", err), rErr)
	}
	if mongo.IsDuplicateKeyError(err) {
		_, err = ms.cspAttestations.UpdateOne(ctx, filter, addQuestion)
	}
	if err != nil {
		return fmt.Errorf("failed to record attested signature: %w", err)
	}
	return nil
}

// attestedRecordRechecks and attestedRecordRecheckDelay bound how long a voter refused a slot waits
// for a concurrent first sign of the same voter, which may hold the last slot, to write its record.
const (
	attestedRecordRechecks     = 5
	attestedRecordRecheckDelay = 20 * time.Millisecond
)

// addQuestionOrRefuse adds the question to the voter's record once a concurrent first sign of the
// same voter wrote it, and returns ErrAttestedCensusFull when no record shows up in time.
func (ms *MongoStorage) addQuestionOrRefuse(ctx context.Context, filter, addQuestion bson.M) error {
	for i := range attestedRecordRechecks {
		if i > 0 {
			time.Sleep(attestedRecordRecheckDelay)
		}
		res, err := ms.cspAttestations.UpdateOne(ctx, filter, addQuestion)
		if err != nil {
			return fmt.Errorf("failed to record attested signature: %w", err)
		}
		if res.MatchedCount == 1 {
			return nil
		}
	}
	return ErrAttestedCensusFull
}

// claimAttestedVoter atomically takes one of the maxVoters attested voter slots of processID,
// reporting false when all are taken. Like ClaimCSPCounter, the conditional upsert collides on _id
// once the count reaches the cap, and one retry tells that apart from losing a concurrent first claim.
func (ms *MongoStorage) claimAttestedVoter(ctx context.Context, processID bson.ObjectID, maxVoters int64) (bool, error) {
	filter := bson.M{"_id": attestedVotersCounterID(processID), "count": bson.M{"$lt": maxVoters}}
	update := bson.M{"$inc": bson.M{"count": int64(1)}}
	opts := options.UpdateOne().SetUpsert(true)
	for attempt := 0; ; attempt++ {
		_, err := ms.counters.UpdateOne(ctx, filter, update, opts)
		if err == nil {
			return true, nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return false, fmt.Errorf("failed to claim an attested voter slot: %w", err)
		}
		if attempt > 0 {
			return false, nil
		}
	}
}

// releaseAttestedVoter gives back a slot claimAttestedVoter took, never below zero.
func (ms *MongoStorage) releaseAttestedVoter(ctx context.Context, processID bson.ObjectID) error {
	filter := bson.M{"_id": attestedVotersCounterID(processID), "count": bson.M{"$gt": 0}}
	if _, err := ms.counters.UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"count": int64(-1)}}); err != nil {
		return fmt.Errorf("failed to release an attested voter slot: %w", err)
	}
	return nil
}

// AttestedCensusFull reports whether the attested voter userID of processID would be refused a
// record: it has none yet and maxVoters voters already have one. It lets auth/0 refuse before an OTP
// is spent; RecordAttestedSignature is the authoritative check.
func (ms *MongoStorage) AttestedCensusFull(processID bson.ObjectID, userID internal.HexBytes, maxVoters int64) (bool, error) {
	if _, err := ms.AttestationRecord(processID, userID); err == nil {
		return false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var counter struct {
		Count int64 `bson:"count"`
	}
	err := ms.counters.FindOne(ctx, bson.M{"_id": attestedVotersCounterID(processID)}).Decode(&counter)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return false, fmt.Errorf("failed to read attested voter count: %w", err)
	}
	return counter.Count >= maxVoters, nil
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
