package migrations

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

// This file deliberately does NOT call AddMigration.
//
// The repair it implements is a one-shot historical backfill, run through
// scripts/repairlogins, not a registered migration. Registering it would write a
// version into the migration ledger, and RunMigrationsUp tracks only the highest
// applied version: any lower-numbered migration that the branch later absorbs
// would be skipped silently, with no error and no warning. The lts ledger has to
// stay below main's for main's migrations to still apply after a forward merge,
// so the backfill stays out of it.
//
// The function is idempotent, so running it more than once is harmless. On main,
// migration 0025 runs it to move every hash to internal.HashLoginFields.

const (
	// memberFetchBatch bounds the $in used to resolve a census's members.
	memberFetchBatch = 1000
	// bulkWriteBatch bounds each bulk update sent to the server.
	bulkWriteBatch = 500
)

// trimmedMemberFields are the member fields that feed the CSP login hash and are
// stored verbatim (unlike email and birthdate, which have their own normalizers).
// Surrounding whitespace in any of them makes the member impossible to
// authenticate, because the login hash is a byte-exact match.
var trimmedMemberFields = []string{"name", "surname", "memberNumber", "nationalId"}

// RepairOptions configures a RepairLoginHashes run.
type RepairOptions struct {
	// Apply performs the writes. When false the run is a dry run: it reports
	// exactly what it would change, including collisions, but writes nothing.
	Apply bool
	// OrgAddress restricts the run to a single organization. Nil scans every
	// organization.
	OrgAddress *common.Address
	// UnsetUnresolved removes the stale login hashes of participants skipped
	// because of a collision, instead of leaving them in place. A stale hash is
	// one that differs from what the service computes today; for a row written
	// by the old code it is the old format, which embeds the lowercased login
	// field values in near-plaintext. No login can match a stale hash, so
	// unsetting it does not lock out anyone who could log in before; the voter
	// stays unable to log in until an admin resolves the duplicate (edits or
	// removes one of the colliding members) and the repair is re-run or the
	// member is edited again. It is the same state the service leaves a
	// participant in when an edit drops the data it needs to log in. Only takes
	// effect with Apply; a dry run counts what it would unset.
	UnsetUnresolved bool
}

// SkippedMember identifies a census participant left untouched because its
// recomputed login hash would collide with another participant of the same
// census. It carries identifiers only, never field values, so a report can be
// shared without disclosing member data.
type SkippedMember struct {
	MemberID string
	CensusID string
	// StaleFields names the stored hash fields ("loginHash", "loginHashEmail",
	// "loginHashPhone") whose value differs from what the service computes today,
	// i.e. the ones UnsetUnresolved removes. Empty when the row already holds
	// current hashes and only other rows of its group are stale.
	StaleFields []string
}

// CollisionGroup is a set of participants of one census whose recomputed login
// hashes collide with each other, directly or through a chain of shared values.
// All of them are skipped together. Like SkippedMember it carries identifiers
// only, so it can be shared to have an admin resolve the duplicates.
type CollisionGroup struct {
	CensusID string
	// MemberIDs are the colliding members, sorted.
	MemberIDs []string
	// OrphanMemberIDs are participant rows of the group whose member document no
	// longer exists; the others collide with their stored hash. Sorted.
	OrphanMemberIDs []string
}

// RepairReport summarizes a RepairLoginHashes run.
type RepairReport struct {
	// MembersScanned counts members matching the whitespace prefilter (phase A).
	MembersScanned int
	// MembersTrimmed counts members whose fields were (or would be) rewritten.
	MembersTrimmed int
	// CensusesScanned counts censuses that have at least one participant.
	CensusesScanned int
	// ParticipantsScanned counts every participant row examined (phase B).
	ParticipantsScanned int
	// ParticipantsRehashed counts rows whose stored hashes were (or would be)
	// rewritten because the recomputed value differs.
	ParticipantsRehashed int
	// ParticipantsSkipped counts rows left alone because of a hash collision.
	ParticipantsSkipped int
	// OrphanParticipants counts rows whose member document no longer exists.
	// Their hashes cannot be recomputed; those voters cannot authenticate today
	// either, because the CSP loads the member after matching the hash.
	OrphanParticipants int
	// CensusesAffected counts censuses containing at least one rehashed row.
	CensusesAffected int
	// SkippedMembers lists the collisions behind ParticipantsSkipped.
	SkippedMembers []SkippedMember
	// CollisionGroups lists the skipped members grouped with the rows they
	// collide with, per census, so an admin can tell which members are duplicates.
	CollisionGroups []CollisionGroup
	// ParticipantsUnset counts skipped rows whose stale hashes were (or, on a dry
	// run, would be) unset because of RepairOptions.UnsetUnresolved.
	ParticipantsUnset int
}

// RepairLoginHashes brings stored CSP login data in line with how the service
// now computes login hashes, in two phases over one run:
//
//	Phase A — strip leading and trailing whitespace from the member fields that
//	feed the login hash. Whitespace picked up from a spreadsheet import makes a
//	member impossible to authenticate, because the hash is a byte-exact match.
//
//	Phase B — recompute every census participant's login hashes and rewrite the
//	ones that changed, so that censuses published before the hash inputs were
//	folded to lowercase keep working and become case-insensitive.
//
// Phase B computes from the trimmed member values whether or not phase A wrote
// them, so a dry run reports exactly what an --apply run would do.
//
// Phase B works one census at a time: it loads that census's participants,
// resolves their members in batches, computes the new hashes in memory and
// groups them. Any group of two or more rows sharing a recomputed hash is
// skipped whole and reported, because the unique index from migration 8 would
// reject the second write. The report groups the skipped rows (CollisionGroups)
// so the duplicates can be resolved by hand; with UnsetUnresolved their stale
// hashes are removed rather than kept.
//
// That grouping is also what makes rewriting in place safe. Writes are not
// transactional (multi-document transactions need a replica set, which is not
// assumed here), so a row could in principle collide with a row not yet
// rewritten. It cannot: the stored hash is derived from the field values, so if
// one row's new hash equalled another's stored hash, that other row's values
// would already be folded and its own hash unchanged — putting both rows in the
// same group, where both are skipped. Rows whose member is missing are the one
// case that cannot be recomputed, so their stored hashes are reserved up front
// and treated as occupied.
//
// The function is idempotent: a second run finds nothing left to trim and every
// hash already equal to its recomputed value, so it writes nothing.
func RepairLoginHashes(ctx context.Context, database *mongo.Database, opts RepairOptions) (RepairReport, error) {
	var report RepairReport

	if err := trimMemberDocuments(ctx, database, opts, &report); err != nil {
		return report, err
	}
	if err := rehashAllCensuses(ctx, database, opts, &report); err != nil {
		return report, err
	}

	log.Infow("login hash repair complete",
		"apply", opts.Apply,
		"membersScanned", report.MembersScanned,
		"membersTrimmed", report.MembersTrimmed,
		"censusesScanned", report.CensusesScanned,
		"participantsScanned", report.ParticipantsScanned,
		"participantsRehashed", report.ParticipantsRehashed,
		"participantsSkipped", report.ParticipantsSkipped,
		"orphanParticipants", report.OrphanParticipants,
		"censusesAffected", report.CensusesAffected,
		"collisionGroups", len(report.CollisionGroups),
		"unsetUnresolved", opts.UnsetUnresolved,
		"participantsUnset", report.ParticipantsUnset)
	return report, nil
}

// trimMemberDocuments is phase A: it rewrites member documents whose login
// fields carry surrounding whitespace. It does not touch login hashes; phase B
// recomputes every hash from the trimmed values regardless.
func trimMemberDocuments(
	ctx context.Context, database *mongo.Database, opts RepairOptions, report *RepairReport,
) error {
	orgMembers := database.Collection("orgMembers")

	cursor, err := orgMembers.Find(ctx, untrimmedMemberFilter(opts.OrgAddress))
	if err != nil {
		return fmt.Errorf("failed to list members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	for cursor.Next(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var member memberHashDoc
		if err := cursor.Decode(&member); err != nil {
			return fmt.Errorf("failed to decode member: %w", err)
		}
		report.MembersScanned++

		trimmed := trimMemberDoc(member)
		if !trimmedMemberDiffers(member, trimmed) {
			continue // the regex is only a coarse prefilter
		}
		report.MembersTrimmed++
		if !opts.Apply {
			continue
		}
		if _, err := orgMembers.UpdateOne(ctx,
			bson.M{"_id": member.ID}, //nolint:goconst
			bson.M{"$set": bson.M{ //nolint:goconst
				"name":         trimmed.Name,
				"surname":      trimmed.Surname,
				"memberNumber": trimmed.MemberNumber,
				"nationalId":   trimmed.NationalID,
				"updatedAt":    time.Now(),
			}},
		); err != nil {
			return fmt.Errorf("failed to trim fields for member %s: %w", member.ID.Hex(), err)
		}
	}
	return cursor.Err()
}

// rehashAllCensuses is phase B: it walks every census and rewrites the login
// hashes that no longer match what the service would compute.
func rehashAllCensuses(
	ctx context.Context, database *mongo.Database, opts RepairOptions, report *RepairReport,
) error {
	filter := bson.M{}
	if opts.OrgAddress != nil {
		filter["orgAddress"] = *opts.OrgAddress
	}
	cursor, err := database.Collection("census").Find(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list censuses: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	for cursor.Next(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var census censusHashDoc
		if err := cursor.Decode(&census); err != nil {
			return fmt.Errorf("failed to decode census: %w", err)
		}
		if err := rehashCensus(ctx, database, census, opts, report); err != nil {
			return err
		}
	}
	return cursor.Err()
}

// participantHashRow is the stored state of one census participant.
type participantHashRow struct {
	ParticipantID  string `bson:"participantID"`
	CensusID       string `bson:"censusId"`
	LoginHash      []byte `bson:"loginHash"`
	LoginHashEmail []byte `bson:"loginHashEmail"`
	LoginHashPhone []byte `bson:"loginHashPhone"`
}

// rehashCensus recomputes and rewrites the login hashes of a single census.
func rehashCensus(
	ctx context.Context,
	database *mongo.Database,
	census censusHashDoc,
	opts RepairOptions,
	report *RepairReport,
) error {
	participants := database.Collection("censusParticipants")
	censusID := census.ID.Hex()

	cur, err := participants.Find(ctx, bson.M{"censusId": censusID}) //nolint:goconst
	if err != nil {
		return fmt.Errorf("failed to list participants of census %s: %w", censusID, err)
	}
	var rows []participantHashRow
	if err := cur.All(ctx, &rows); err != nil {
		return fmt.Errorf("failed to decode participants of census %s: %w", censusID, err)
	}
	if len(rows) == 0 {
		return nil
	}
	report.CensusesScanned++
	report.ParticipantsScanned += len(rows)

	members, err := loadMembersFor(ctx, database, rows)
	if err != nil {
		return err
	}

	type pending struct {
		participantID string
		hashes        participantHashSet
		stale         []string
		changed       bool
	}
	var pendings []pending
	// occupied maps a hash field to the set of values already claimed within this
	// census, either by another recomputed row or by a row that cannot be
	// recomputed because its member is gone.
	occupied := map[string]map[string][]hashClaimant{}
	claim := func(field string, value []byte, participantID string, orphan bool) {
		if len(value) == 0 {
			return
		}
		if occupied[field] == nil {
			occupied[field] = map[string][]hashClaimant{}
		}
		key := hex.EncodeToString(value)
		occupied[field][key] = append(occupied[field][key],
			hashClaimant{participantID: participantID, orphan: orphan})
	}

	for _, row := range rows {
		member, ok := members[row.ParticipantID]
		if !ok {
			// The member is gone, so the hash cannot be recomputed. The row keeps
			// its stored hashes, which stay reserved so nothing else claims them.
			report.OrphanParticipants++
			claim("loginHash", row.LoginHash, row.ParticipantID, true)
			claim("loginHashEmail", row.LoginHashEmail, row.ParticipantID, true)
			claim("loginHashPhone", row.LoginHashPhone, row.ParticipantID, true)
			continue
		}
		// Compute from the trimmed member so a dry run matches an --apply run,
		// whether or not phase A has already written the trimmed values.
		hashes := computeParticipantHashes(trimMemberDoc(member), census)
		pendings = append(pendings, pending{
			participantID: row.ParticipantID,
			hashes:        hashes,
			stale:         hashes.staleFields(row),
			changed:       hashes.differsFrom(row),
		})
		claim("loginHash", hashes.LoginHash, row.ParticipantID, false)
		claim("loginHashEmail", hashes.LoginHashEmail, row.ParticipantID, false)
		claim("loginHashPhone", hashes.LoginHashPhone, row.ParticipantID, false)
	}

	skipped, groups := collisionSkips(occupied)
	for _, g := range groups {
		g.CensusID = censusID
		report.CollisionGroups = append(report.CollisionGroups, g)
		log.Warnw("skipping participants: recomputed login hashes collide within the census",
			"censusID", censusID, "memberIDs", g.MemberIDs, "orphanMemberIDs", g.OrphanMemberIDs)
	}
	var models []mongo.WriteModel
	censusCounted := false
	for _, p := range pendings {
		if _, bad := skipped[p.participantID]; bad {
			report.ParticipantsSkipped++
			report.SkippedMembers = append(report.SkippedMembers,
				SkippedMember{MemberID: p.participantID, CensusID: censusID, StaleFields: p.stale})
			if !opts.UnsetUnresolved || len(p.stale) == 0 {
				continue
			}
			report.ParticipantsUnset++
			log.Warnw("unsetting stale login hashes of a colliding participant",
				"memberID", p.participantID, "censusID", censusID, "fields", p.stale, "apply", opts.Apply)
			if opts.Apply {
				models = append(models, unsetStaleModel(p.participantID, censusID, p.stale))
			}
			continue
		}
		if !p.changed {
			continue
		}
		report.ParticipantsRehashed++
		if !censusCounted {
			report.CensusesAffected++
			censusCounted = true
		}
		if !opts.Apply {
			continue
		}
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"participantID": p.participantID, "censusId": censusID}).
			SetUpdate(bson.M{"$set": p.hashes.bson(time.Now())})) //nolint:goconst
	}

	return flushBulk(ctx, participants, models, censusID)
}

// loadMembersFor resolves the member document of every participant row, in
// batches, so a large census costs a handful of queries rather than one per row.
func loadMembersFor(
	ctx context.Context, database *mongo.Database, rows []participantHashRow,
) (map[string]memberHashDoc, error) {
	ids := make([]bson.ObjectID, 0, len(rows))
	for _, row := range rows {
		oid, err := bson.ObjectIDFromHex(row.ParticipantID)
		if err != nil {
			continue // malformed id: treated as an orphan below
		}
		ids = append(ids, oid)
	}

	members := make(map[string]memberHashDoc, len(ids))
	orgMembers := database.Collection("orgMembers")
	for start := 0; start < len(ids); start += memberFetchBatch {
		end := min(start+memberFetchBatch, len(ids))
		cur, err := orgMembers.Find(ctx, bson.M{"_id": bson.M{"$in": ids[start:end]}})
		if err != nil {
			return nil, fmt.Errorf("failed to load members: %w", err)
		}
		var batch []memberHashDoc
		if err := cur.All(ctx, &batch); err != nil {
			return nil, fmt.Errorf("failed to decode members: %w", err)
		}
		for _, m := range batch {
			members[m.ID.Hex()] = m
		}
	}
	return members, nil
}

// hashClaimant is a participant row claiming a hash value within a census.
// Orphan rows claim their stored value; every other row its recomputed one.
type hashClaimant struct {
	participantID string
	orphan        bool
}

// collisionSkips returns the participants that must be left alone because two or
// more rows in the census claim the same value for a hash field, together with
// those rows grouped into connected components: two rows share a group when they
// claim the same value on any field, directly or through other rows. Orphan rows
// are never skipped (they are not rewritten anyway) but are listed in the group
// they collide with. The groups carry no CensusID; the caller sets it.
func collisionSkips(occupied map[string]map[string][]hashClaimant) (map[string]struct{}, []CollisionGroup) {
	parent := map[string]string{}
	var find func(string) string
	find = func(id string) string {
		if parent[id] != id {
			parent[id] = find(parent[id])
		}
		return parent[id]
	}
	orphans := map[string]bool{}
	skipped := map[string]struct{}{}
	for _, byValue := range occupied {
		for _, claimants := range byValue {
			if len(claimants) < 2 {
				continue
			}
			for _, cl := range claimants {
				if _, ok := parent[cl.participantID]; !ok {
					parent[cl.participantID] = cl.participantID
				}
				if cl.orphan {
					orphans[cl.participantID] = true
				} else {
					skipped[cl.participantID] = struct{}{}
				}
			}
			root := find(claimants[0].participantID)
			for _, cl := range claimants[1:] {
				if r := find(cl.participantID); r != root {
					parent[r] = root
				}
			}
		}
	}

	byRoot := map[string]*CollisionGroup{}
	for id := range parent {
		root := find(id)
		g := byRoot[root]
		if g == nil {
			g = &CollisionGroup{}
			byRoot[root] = g
		}
		if orphans[id] {
			g.OrphanMemberIDs = append(g.OrphanMemberIDs, id)
		} else {
			g.MemberIDs = append(g.MemberIDs, id)
		}
	}
	groups := make([]CollisionGroup, 0, len(byRoot))
	for _, g := range byRoot {
		if len(g.MemberIDs) == 0 {
			continue // only orphans: nothing is skipped, nothing to resolve
		}
		slices.Sort(g.MemberIDs)
		slices.Sort(g.OrphanMemberIDs)
		groups = append(groups, *g)
	}
	slices.SortFunc(groups, func(a, b CollisionGroup) int {
		return strings.Compare(a.MemberIDs[0], b.MemberIDs[0])
	})
	return skipped, groups
}

// unsetStaleModel builds the update removing a skipped participant's stale hash
// fields, as db.updateCensusParticipantsForMember drops the hashes of a member
// left without login data. The login hash unique indexes are partial (they only
// cover binData values, see migration 8), so any number of rows in a census can
// lack a field without clashing.
func unsetStaleModel(participantID, censusID string, fields []string) mongo.WriteModel {
	unset := bson.M{}
	for _, f := range fields {
		unset[f] = ""
	}
	return mongo.NewUpdateOneModel().
		SetFilter(bson.M{"participantID": participantID, "censusId": censusID}).
		SetUpdate(bson.M{"$unset": unset, "$set": bson.M{"updatedAt": time.Now()}}) //nolint:goconst
}

// flushBulk writes the pending updates in bounded batches.
func flushBulk(
	ctx context.Context, participants *mongo.Collection, models []mongo.WriteModel, censusID string,
) error {
	for start := 0; start < len(models); start += bulkWriteBatch {
		end := min(start+bulkWriteBatch, len(models))
		if _, err := participants.BulkWrite(ctx, models[start:end]); err != nil {
			return fmt.Errorf("failed to rewrite login hashes for census %s: %w", censusID, err)
		}
	}
	return nil
}

// participantHashSet holds the login hash variants stored for one participant.
type participantHashSet struct {
	LoginHash      []byte
	LoginHashEmail []byte
	LoginHashPhone []byte
}

// computeParticipantHashes mirrors db.calculateParticipantHashesBson: it produces
// exactly the hash fields that were originally stored for a participant, so the
// repair never adds or removes keys.
func computeParticipantHashes(m memberHashDoc, c censusHashDoc) participantHashSet {
	set := participantHashSet{
		LoginHash: hashMemberFields(m, c.AuthFields, c.TwoFaFields),
	}
	if len(c.TwoFaFields) == 2 && strings.TrimSpace(m.Email) != "" {
		set.LoginHashEmail = hashMemberFields(m, c.AuthFields, []string{"email"}) //nolint:goconst
	}
	if len(c.TwoFaFields) == 2 && len(m.Phone) > 0 {
		set.LoginHashPhone = hashMemberFields(m, c.AuthFields, []string{"phone"})
	}
	return set
}

// differsFrom reports whether the recomputed hashes differ from what is stored.
func (s participantHashSet) differsFrom(row participantHashRow) bool {
	return !bytesEqual(s.LoginHash, row.LoginHash) ||
		!bytesEqual(s.LoginHashEmail, row.LoginHashEmail) ||
		!bytesEqual(s.LoginHashPhone, row.LoginHashPhone)
}

// staleFields names the stored hash fields holding a value other than the one
// recomputed for them. Fields stored empty are not stale: there is nothing to
// remove.
func (s participantHashSet) staleFields(row participantHashRow) []string {
	var stale []string
	if len(row.LoginHash) > 0 && !bytesEqual(s.LoginHash, row.LoginHash) {
		stale = append(stale, "loginHash")
	}
	if len(row.LoginHashEmail) > 0 && !bytesEqual(s.LoginHashEmail, row.LoginHashEmail) {
		stale = append(stale, "loginHashEmail")
	}
	if len(row.LoginHashPhone) > 0 && !bytesEqual(s.LoginHashPhone, row.LoginHashPhone) {
		stale = append(stale, "loginHashPhone")
	}
	return stale
}

// bson renders the set as an update document, omitting the variants a census
// does not store so they are neither created nor cleared.
func (s participantHashSet) bson(now time.Time) bson.M {
	set := bson.M{"loginHash": s.LoginHash, "updatedAt": now} //nolint:goconst
	if len(s.LoginHashEmail) > 0 {
		set["loginHashEmail"] = s.LoginHashEmail
	}
	if len(s.LoginHashPhone) > 0 {
		set["loginHashPhone"] = s.LoginHashPhone
	}
	return set
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
