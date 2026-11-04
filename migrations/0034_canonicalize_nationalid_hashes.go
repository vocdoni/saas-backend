package migrations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// NationalIDNormalizationVersion is the version of migration 0034, which canonicalizes the
// nationalId login hashes. The certificate census publish guard asks db.MigrationApplied for it,
// since a branch merge could absorb it under a higher ledger version and skip it silently.
const NationalIDNormalizationVersion = 34

const (
	// nationalIDBatchSize is how many candidate members one batch of migration 0034 reads.
	nationalIDBatchSize = 500
	// nationalIDBatchTimeout bounds one batch of migration 0034. It is well under the migration
	// lock TTL, which every batch renews first, so the lock cannot expire under a live run.
	nationalIDBatchTimeout = 2 * time.Minute
)

func init() {
	AddMigration(34, "canonicalize_nationalid_login_hashes", upCanonicalizeNationalIDHashes,
		downCanonicalizeNationalIDHashes)
}

// nationalIDCandidates selects the members whose nationalId may not be canonical yet, after lastID
// in _id order: a non-empty string that is not already shaped like an uppercase DNI/NIE. It is a
// coarse prefilter; NormalizeNationalID decides in Go.
func nationalIDCandidates(lastID bson.ObjectID) bson.M {
	filter := bson.M{"nationalId": bson.M{
		"$type": "string",
		"$ne":   "",
		"$not":  bson.Regex{Pattern: "^([0-9]{8}|[XYZ][0-9]{7})[A-Z]$"},
	}}
	if !lastID.IsZero() {
		filter["_id"] = bson.M{"$gt": lastID}
	}
	return filter
}

// nationalIDRehasher holds the state of one run of migration 0034.
type nationalIDRehasher struct {
	members      *mongo.Collection
	participants *mongo.Collection
	censuses     *mongo.Collection
	// censusCache maps a census hex id to its auth configuration, nil when it does not exist
	censusCache       map[string]*censusHashDoc
	rehashed, skipped int
}

// upCanonicalizeNationalIDHashes recomputes the login hashes of the census participants of every
// member stored with a DNI/NIE in a non-canonical form (separators, missing zero padding, missing
// check letter), in every census logging in with the nationalId, so any variant typed at login
// matches. It never writes orgMembers: the stored values stay exactly as entered, and only the
// hash input is canonicalized (recomputeParticipantHashes, as db.calculateParticipantHashes does).
//
// Each participant is its own unit: one whose stored hashes are already the canonical ones is left
// alone, and one whose canonical hash another participant of the census holds is skipped and logged
// (by ids only, never the value), keeping its raw hash, which the auth lookup still resolves
// verbatim. Since members never change, a re-run re-derives the same candidates and finds nothing
// left to write, so an interrupted run is resumed by running it again.
//
// It works in keyset-paginated batches, each under its own timeout instead of the run's, and
// renews the migration lock before each one, so it does not depend on finishing within the run
// timeout or the lock TTL. A lost lock aborts it before the batch writes anything.
//
// Rollback caveat: scripts/repairlogins built at an older version recomputes the raw hashes, undoing
// this migration while it stays recorded in the ledger. After such a repair the ledger no longer
// proves canonical hashes; fix forward by running the current scripts/repairlogins.
func upCanonicalizeNationalIDHashes(ctx context.Context, database *mongo.Database) error {
	n := &nationalIDRehasher{
		members:      database.Collection("orgMembers"),
		participants: database.Collection("censusParticipants"),
		censuses:     database.Collection("census"),
		censusCache:  make(map[string]*censusHashDoc),
	}
	lastID := bson.NilObjectID
	for {
		next, more, err := n.batch(ctx, lastID)
		if err != nil {
			return err
		}
		if !more {
			break
		}
		lastID = next
	}
	log.Infow("canonicalized nationalId login hashes", "rehashed", n.rehashed, "skipped", n.skipped)
	return nil
}

// batch rehashes the participants of the next nationalIDBatchSize candidates after lastID,
// returning the last id it visited and whether a full batch was read (so more may follow).
func (n *nationalIDRehasher) batch(parent context.Context, lastID bson.ObjectID) (bson.ObjectID, bool, error) {
	// drop the run timeout, keep the context values (the lock renewer)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), nationalIDBatchTimeout)
	defer cancel()
	if err := RenewLock(ctx); err != nil {
		return lastID, false, fmt.Errorf("failed to renew the migration lock: %w", err)
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetLimit(nationalIDBatchSize)
	cur, err := n.members.Find(ctx, nationalIDCandidates(lastID), opts)
	if err != nil {
		return lastID, false, fmt.Errorf("failed to list members: %w", err)
	}
	var members []memberHashDoc
	if err := cur.All(ctx, &members); err != nil {
		return lastID, false, fmt.Errorf("failed to decode members: %w", err)
	}
	for _, m := range members {
		if err := n.rehashMember(ctx, m); err != nil {
			return lastID, false, err
		}
		lastID = m.ID
	}
	return lastID, len(members) == nationalIDBatchSize, nil
}

// rehashMember gives each of the member's participants in a nationalId census its canonical login
// hashes, unless its value has no canonical form or already hashes canonically.
func (n *nationalIDRehasher) rehashMember(ctx context.Context, m memberHashDoc) error {
	r := internal.NormalizeNationalID(m.NationalID)
	// the hash lowercases its input, so a case-only variant already hashes canonically;
	// letter_mismatch, unsupported_document and invalid values have no canonical form
	if r.Canonical == "" || strings.EqualFold(r.Canonical, m.NationalID) {
		return nil
	}
	memberHex := m.ID.Hex()
	pcur, err := n.participants.Find(ctx, bson.M{"participantID": memberHex})
	if err != nil {
		return fmt.Errorf("failed to find participants for member %s: %w", memberHex, err)
	}
	var participants []participantHashRow
	if err := pcur.All(ctx, &participants); err != nil {
		return fmt.Errorf("failed to decode participants for member %s: %w", memberHex, err)
	}
	for _, p := range participants {
		if err := n.rehashParticipant(ctx, m, p); err != nil {
			return err
		}
	}
	return nil
}

// rehashParticipant writes one participant's canonical login hashes, unless its census does not log
// in with the nationalId, it holds them already, or another participant of the census does.
func (n *nationalIDRehasher) rehashParticipant(ctx context.Context, m memberHashDoc, p participantHashRow) error {
	census, err := n.census(ctx, p.CensusID)
	if err != nil {
		return err
	}
	if census == nil || !slices.Contains(census.AuthFields, "nationalId") {
		return nil // the nationalId takes no part in this census's hashes
	}
	// m is the member as stored: recomputeParticipantHashes canonicalizes only its hash input
	hashes := recomputeParticipantHashes(m, *census)
	if storedHashesEqual(p, hashes) {
		return nil // rehashed by an earlier run, or written by the canonicalizing code
	}

	findHashes := make([]bson.M, 0, len(hashes))
	for k, v := range hashes {
		findHashes = append(findHashes, bson.M{k: v})
	}
	count, err := n.participants.CountDocuments(ctx, bson.M{
		"participantID": bson.M{"$ne": p.ParticipantID},
		"censusId":      p.CensusID,
		"$or":           findHashes,
	})
	if err != nil {
		return fmt.Errorf("failed to check collisions for member %s in census %s: %w", p.ParticipantID, p.CensusID, err)
	}
	if count > 0 {
		// ids only: the value is a DNI
		log.Warnw("skipping participant nationalId login hash due to collision",
			"memberID", p.ParticipantID, "censusID", p.CensusID)
		n.skipped++
		return nil
	}

	set := bson.M{"updatedAt": time.Now()}
	for k, v := range hashes {
		set[k] = v
	}
	if _, err := n.participants.UpdateOne(ctx,
		bson.M{"participantID": p.ParticipantID, "censusId": p.CensusID},
		bson.M{"$set": set},
	); err != nil {
		return fmt.Errorf("failed to update participant hashes for member %s in census %s: %w",
			p.ParticipantID, p.CensusID, err)
	}
	n.rehashed++
	return nil
}

// storedHashesEqual reports whether the participant already stores every hash in hashes. Only those
// keys are compared, the ones the update would set; an absent stored key counts as empty.
func storedHashesEqual(p participantHashRow, hashes bson.M) bool {
	stored := map[string][]byte{
		"loginHash":      p.LoginHash,
		"loginHashEmail": p.LoginHashEmail,
		"loginHashPhone": p.LoginHashPhone,
	}
	for k, v := range hashes {
		want, ok := v.([]byte)
		if !ok || !bytesEqual(stored[k], want) {
			return false
		}
	}
	return true
}

// census returns the auth configuration of a census, cached, or nil when it does not exist.
func (n *nationalIDRehasher) census(ctx context.Context, hexID string) (*censusHashDoc, error) {
	if c, ok := n.censusCache[hexID]; ok {
		return c, nil
	}
	oid, err := bson.ObjectIDFromHex(hexID)
	if err != nil {
		n.censusCache[hexID] = nil
		return nil, nil
	}
	var c censusHashDoc
	err = n.censuses.FindOne(ctx, bson.M{"_id": oid}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		n.censusCache[hexID] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load census %s: %w", hexID, err)
	}
	n.censusCache[hexID] = &c
	return &c, nil
}

// downCanonicalizeNationalIDHashes is intentionally a no-op. Members are untouched, so the raw
// hashes are derivable from the stored values: scripts/repairlogins built at the version before
// this migration recomputes exactly those (see the rollback caveat on the up migration).
func downCanonicalizeNationalIDHashes(_ context.Context, _ *mongo.Database) error {
	return nil
}
