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

// NationalIDNormalizationVersion is the version of migration 0034, which canonicalizes stored
// nationalIds. The certificate census publish guard asks db.MigrationApplied for it, since a
// branch merge could absorb it under a higher ledger version and skip it silently.
const NationalIDNormalizationVersion = 34

const (
	// nationalIDBatchSize is how many candidate members one batch of migration 0034 reads.
	nationalIDBatchSize = 500
	// nationalIDBatchTimeout bounds one batch of migration 0034. It is well under the migration
	// lock TTL, which every batch renews first, so the lock cannot expire under a live run.
	nationalIDBatchTimeout = 2 * time.Minute
)

func init() {
	AddMigration(34, "normalize_national_ids", upNormalizeNationalIDs, downNormalizeNationalIDs)
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

// nationalIDNormalizer holds the state of one run of migration 0034.
type nationalIDNormalizer struct {
	members      *mongo.Collection
	participants *mongo.Collection
	censuses     *mongo.Collection
	// censusCache maps a census hex id to its auth configuration, nil when it does not exist
	censusCache                  map[string]*censusHashDoc
	normalized, derived, skipped int
}

// upNormalizeNationalIDs canonicalizes every stored nationalId that is a DNI/NIE in a
// non-canonical form (separators, lowercase, missing zero padding, missing check letter) and
// recomputes the login hashes of the member's participants in every census logging in with the
// nationalId. Values that are not a valid DNI/NIE are left exactly as stored.
//
// It follows migration 0015: a member whose new hash would collide with another participant of a
// census is skipped whole and logged (by id only, never the value), so it keeps matching its own
// input and is re-detected on a later run; and the participants are written before the member, so
// an interrupted member is re-detected too.
//
// It works in keyset-paginated batches, each under its own timeout instead of the run's, and
// renews the migration lock before each one, so it does not depend on finishing within the run
// timeout or the lock TTL. A lost lock aborts it before the batch writes anything.
func upNormalizeNationalIDs(ctx context.Context, database *mongo.Database) error {
	n := &nationalIDNormalizer{
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
	log.Infow("normalized member nationalIds", "normalized", n.normalized, "derived", n.derived, "skipped", n.skipped)
	return nil
}

// batch normalizes the next nationalIDBatchSize candidates after lastID, returning the last id it
// visited and whether a full batch was read (so more may follow).
func (n *nationalIDNormalizer) batch(parent context.Context, lastID bson.ObjectID) (bson.ObjectID, bool, error) {
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
		if err := n.normalizeMember(ctx, m); err != nil {
			return lastID, false, err
		}
		lastID = m.ID
	}
	return lastID, len(members) == nationalIDBatchSize, nil
}

// normalizeMember canonicalizes one member's nationalId and its participants' login hashes,
// unless the value has no canonical form, is canonical already, or a hash would collide.
func (n *nationalIDNormalizer) normalizeMember(ctx context.Context, m memberHashDoc) error {
	r := internal.NormalizeNationalID(m.NationalID)
	if r.Canonical == "" || r.Canonical == m.NationalID {
		return nil // letter_mismatch, unsupported_document, invalid: left exactly as stored
	}
	memberHex := m.ID.Hex()
	canonical := m
	canonical.NationalID = r.Canonical

	pcur, err := n.participants.Find(ctx, bson.M{"participantID": memberHex})
	if err != nil {
		return fmt.Errorf("failed to find participants for member %s: %w", memberHex, err)
	}
	var participants []participantHashDoc
	if err := pcur.All(ctx, &participants); err != nil {
		return fmt.Errorf("failed to decode participants for member %s: %w", memberHex, err)
	}

	type participantUpdate struct {
		filter bson.M
		hashes bson.M
	}
	updates := make([]participantUpdate, 0, len(participants))
	for _, p := range participants {
		census, err := n.census(ctx, p.CensusID)
		if err != nil {
			return err
		}
		if census == nil || !slices.Contains(census.AuthFields, "nationalId") {
			continue // the nationalId takes no part in this census's hashes
		}
		hashes := recomputeParticipantHashes(canonical, *census)
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
			return fmt.Errorf("failed to check collisions for member %s in census %s: %w", memberHex, p.CensusID, err)
		}
		if count > 0 {
			// ids only: the value is a DNI
			log.Warnw("skipping member nationalId normalization due to login hash collision",
				"memberID", memberHex, "censusID", p.CensusID)
			n.skipped++
			return nil
		}
		updates = append(updates, participantUpdate{
			filter: bson.M{"participantID": p.ParticipantID, "censusId": p.CensusID},
			hashes: hashes,
		})
	}

	// participants first, then the member: an interrupted member keeps its old value and is
	// re-detected on the next run
	now := time.Now()
	for _, u := range updates {
		set := bson.M{"updatedAt": now}
		for k, v := range u.hashes {
			set[k] = v
		}
		if _, err := n.participants.UpdateOne(ctx, u.filter, bson.M{"$set": set}); err != nil {
			return fmt.Errorf("failed to update participant hashes for member %s: %w", memberHex, err)
		}
	}
	set := bson.M{"nationalId": r.Canonical, "updatedAt": now}
	if r.Class == internal.NationalIDDerivedLetter {
		set["nationalIdOriginal"] = strings.TrimSpace(m.NationalID)
		n.derived++
	}
	if _, err := n.members.UpdateOne(ctx, bson.M{"_id": m.ID}, bson.M{"$set": set}); err != nil {
		return fmt.Errorf("failed to update nationalId for member %s: %w", memberHex, err)
	}
	n.normalized++
	return nil
}

// census returns the auth configuration of a census, cached, or nil when it does not exist.
func (n *nationalIDNormalizer) census(ctx context.Context, hexID string) (*censusHashDoc, error) {
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

// downNormalizeNationalIDs is intentionally a no-op: the normalization is lossy (the original
// separators, case and padding cannot be recovered), like migrations 0015 and 0025.
func downNormalizeNationalIDs(_ context.Context, _ *mongo.Database) error {
	return nil
}
