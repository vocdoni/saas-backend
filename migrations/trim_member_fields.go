package migrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// This file deliberately does NOT call AddMigration.
//
// The fix it implements is a one-shot historical backfill, run through
// scripts/trimmembers, not a registered migration. Registering it would write a
// version into the migration ledger, and RunMigrationsUp tracks only the highest
// applied version: any lower-numbered migration that the branch later absorbs
// would be skipped silently, with no error and no warning. The lts ledger has to
// stay below main's for main's migrations to still apply after a forward merge,
// so the backfill stays out of it.
//
// The function is idempotent, so running it more than once — or running it here
// and again later as a registered migration on another branch — is harmless.

// trimmedMemberFields are the member fields that feed the CSP login hash and are
// stored verbatim (unlike email and birthdate, which have their own normalizers).
// Surrounding whitespace in any of them makes the member impossible to
// authenticate, because the login hash is a byte-exact match.
var trimmedMemberFields = []string{"name", "surname", "memberNumber", "nationalId"}

// TrimOptions configures a TrimMemberFields run.
type TrimOptions struct {
	// Apply performs the writes. When false the run is a dry run: it reports
	// exactly what it would change, including collisions, but writes nothing.
	Apply bool
	// OrgAddress restricts the run to a single organization. Nil scans every
	// organization.
	OrgAddress *common.Address
}

// SkippedMember identifies a member that was left untouched because trimming it
// would make its login hash collide with another participant of the same census.
// It carries identifiers only, never field values, so a report can be shared
// without disclosing member data.
type SkippedMember struct {
	MemberID string
	CensusID string
}

// TrimReport summarizes a TrimMemberFields run.
type TrimReport struct {
	// Scanned is the number of members that matched the whitespace prefilter.
	Scanned int
	// Trimmed is the number of members whose fields were (or would be) rewritten.
	Trimmed int
	// Skipped is the number of members left untouched because of a hash collision.
	Skipped int
	// ParticipantsUpdated is the number of census participant documents whose
	// login hashes were (or would be) recomputed.
	ParticipantsUpdated int
	// CensusesAffected is the number of distinct censuses containing at least one
	// recomputed participant.
	CensusesAffected int
	// OrphanParticipants counts participants whose census no longer exists; their
	// hashes cannot be recomputed and are left alone.
	OrphanParticipants int
	// SkippedMembers lists the collisions behind Skipped, for manual review.
	SkippedMembers []SkippedMember
}

// TrimMemberFields strips leading and trailing whitespace from the member fields
// that feed the CSP login hash, and recomputes the login hashes of every census
// participant derived from those members so that existing censuses keep working.
//
// Members are processed one at a time and each is all-or-nothing: the recomputed
// hashes are checked against the other participants of every census the member
// belongs to before anything is written, and a member whose trimmed hash would
// collide with another participant (rejected by the unique index from migration
// 8) is skipped and reported rather than aborting the whole run.
//
// The function is idempotent: once a member is trimmed it no longer matches the
// prefilter, so a second run is a no-op.
func TrimMemberFields(ctx context.Context, database *mongo.Database, opts TrimOptions) (TrimReport, error) {
	var report TrimReport

	orgMembers := database.Collection("orgMembers")
	censusParticipants := database.Collection("censusParticipants")
	censuses := database.Collection("census")

	getCensus := censusLoader(ctx, censuses)
	affectedCensuses := make(map[string]struct{})

	cursor, err := orgMembers.Find(ctx, untrimmedMemberFilter(opts.OrgAddress))
	if err != nil {
		return report, fmt.Errorf("failed to list members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	for cursor.Next(ctx) {
		if err := ctx.Err(); err != nil {
			return report, err
		}

		var member memberHashDoc
		if err := cursor.Decode(&member); err != nil {
			return report, fmt.Errorf("failed to decode member: %w", err)
		}
		report.Scanned++

		trimmed := trimMemberDoc(member)
		if !trimmedMemberDiffers(member, trimmed) {
			continue // the regex is only a coarse prefilter
		}
		memberHex := member.ID.Hex()

		updates, skipped, err := planParticipantUpdates(ctx, censusParticipants, getCensus, trimmed, &report)
		if err != nil {
			return report, err
		}
		if skipped != nil {
			log.Warnw("skipping member: trimming would collide with another participant's login hash",
				"memberID", skipped.MemberID, "censusID", skipped.CensusID)
			report.Skipped++
			report.SkippedMembers = append(report.SkippedMembers, *skipped)
			continue
		}

		report.Trimmed++
		report.ParticipantsUpdated += len(updates)
		for _, u := range updates {
			affectedCensuses[u.censusID] = struct{}{}
		}
		if !opts.Apply {
			continue
		}

		// Recompute the participant hashes first: a participant carrying a stale
		// hash while the member is already trimmed is what locks a voter out, so
		// the participants must never be the ones left behind on a partial failure.
		now := time.Now()
		for _, u := range updates {
			set := bson.M{"updatedAt": now}
			for k, v := range u.hashes {
				set[k] = v
			}
			if _, err := censusParticipants.UpdateOne(ctx, u.filter, bson.M{"$set": set}); err != nil { //nolint:goconst
				return report, fmt.Errorf("failed to update participant hashes for member %s: %w", memberHex, err)
			}
		}
		if _, err := orgMembers.UpdateOne(ctx,
			bson.M{"_id": member.ID}, //nolint:goconst
			bson.M{"$set": bson.M{
				"name":         trimmed.Name,
				"surname":      trimmed.Surname,
				"memberNumber": trimmed.MemberNumber,
				"nationalId":   trimmed.NationalID,
				"updatedAt":    now,
			}},
		); err != nil {
			return report, fmt.Errorf("failed to trim fields for member %s: %w", memberHex, err)
		}
	}
	if err := cursor.Err(); err != nil {
		return report, fmt.Errorf("cursor error iterating members: %w", err)
	}

	report.CensusesAffected = len(affectedCensuses)
	log.Infow("trimmed member fields",
		"apply", opts.Apply,
		"scanned", report.Scanned,
		"trimmed", report.Trimmed,
		"skipped", report.Skipped,
		"participants", report.ParticipantsUpdated,
		"censuses", report.CensusesAffected,
		"orphanParticipants", report.OrphanParticipants)
	return report, nil
}

// hashRewrite is a pending login-hash rewrite for one census participant.
type hashRewrite struct {
	filter   bson.M
	hashes   bson.M
	censusID string
}

// untrimmedMemberFilter builds the server-side prefilter selecting members whose
// hash-relevant fields have leading or trailing whitespace. The authoritative
// check happens in Go, on the decoded document.
func untrimmedMemberFilter(orgAddress *common.Address) bson.M {
	or := make([]bson.M, 0, len(trimmedMemberFields))
	for _, field := range trimmedMemberFields {
		or = append(or, bson.M{field: bson.M{"$regex": "^\\s|\\s$"}})
	}
	filter := bson.M{"$or": or} //nolint:goconst
	if orgAddress != nil {
		filter["orgAddress"] = *orgAddress
	}
	return filter
}

// trimMemberDoc returns a copy of the member with its hash-relevant fields
// trimmed. Email and birthdate are left alone: they are canonicalized by
// internal.NormalizeEmail and internal.ParseBirthDate at write time.
func trimMemberDoc(m memberHashDoc) memberHashDoc {
	m.Name = strings.TrimSpace(m.Name)
	m.Surname = strings.TrimSpace(m.Surname)
	m.MemberNumber = strings.TrimSpace(m.MemberNumber)
	m.NationalID = strings.TrimSpace(m.NationalID)
	return m
}

// trimmedMemberDiffers reports whether trimming actually changed the member.
// memberHashDoc holds a []byte phone and so is not comparable with ==, and only
// the trimmed fields can differ anyway.
func trimmedMemberDiffers(before, after memberHashDoc) bool {
	return before.Name != after.Name ||
		before.Surname != after.Surname ||
		before.MemberNumber != after.MemberNumber ||
		before.NationalID != after.NationalID
}

// censusLoader returns a memoized census lookup by hex id. It returns a nil
// census (and no error) when the census no longer exists, so callers can treat
// orphaned participants as skippable rather than fatal.
func censusLoader(ctx context.Context, censuses *mongo.Collection) func(string) (*censusHashDoc, error) {
	cache := make(map[string]*censusHashDoc)
	return func(hexID string) (*censusHashDoc, error) {
		if c, ok := cache[hexID]; ok {
			return c, nil
		}
		oid, err := primitive.ObjectIDFromHex(hexID)
		if err != nil {
			cache[hexID] = nil
			return nil, nil //nolint:nilnil // a malformed id is an orphan, not a failure
		}
		var c censusHashDoc
		err = censuses.FindOne(ctx, bson.M{"_id": oid}).Decode(&c)
		if errors.Is(err, mongo.ErrNoDocuments) {
			cache[hexID] = nil
			return nil, nil //nolint:nilnil // a missing census is an orphan, not a failure
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load census %s: %w", hexID, err)
		}
		cache[hexID] = &c
		return &c, nil
	}
}

// planParticipantUpdates computes the login-hash rewrites needed for every census
// the member belongs to, without writing anything. It returns a non-nil
// SkippedMember instead of any updates if one of the recomputed hashes would
// collide with a different participant of the same census.
func planParticipantUpdates(
	ctx context.Context,
	censusParticipants *mongo.Collection,
	getCensus func(string) (*censusHashDoc, error),
	trimmed memberHashDoc,
	report *TrimReport,
) ([]hashRewrite, *SkippedMember, error) {
	memberHex := trimmed.ID.Hex()

	pcur, err := censusParticipants.Find(ctx, bson.M{"participantID": memberHex})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find participants for member %s: %w", memberHex, err)
	}
	var participants []participantHashDoc
	if err := pcur.All(ctx, &participants); err != nil {
		return nil, nil, fmt.Errorf("failed to decode participants for member %s: %w", memberHex, err)
	}

	var updates []hashRewrite
	for _, p := range participants {
		census, err := getCensus(p.CensusID)
		if err != nil {
			return nil, nil, err
		}
		if census == nil {
			// The census is gone, so there is no hash to keep in sync.
			report.OrphanParticipants++
			continue
		}

		hashes := recomputeParticipantHashes(trimmed, *census)
		findHashes := make([]bson.M, 0, len(hashes))
		for k, v := range hashes {
			findHashes = append(findHashes, bson.M{k: v})
		}
		if len(findHashes) == 0 {
			continue
		}

		count, err := censusParticipants.CountDocuments(ctx, bson.M{
			"participantID": bson.M{"$ne": p.ParticipantID}, //nolint:goconst
			"censusId":      p.CensusID,                     //nolint:goconst
			"$or":           findHashes,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to check collisions for member %s in census %s: %w",
				memberHex, p.CensusID, err)
		}
		if count > 0 {
			return nil, &SkippedMember{MemberID: memberHex, CensusID: p.CensusID}, nil
		}

		updates = append(updates, hashRewrite{
			filter:   bson.M{"participantID": p.ParticipantID, "censusId": p.CensusID},
			hashes:   hashes,
			censusID: p.CensusID,
		})
	}
	return updates, nil, nil
}
