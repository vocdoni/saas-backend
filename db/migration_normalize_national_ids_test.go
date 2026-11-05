package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// seededAt is the updatedAt of every seeded participant, so a write by the migration shows.
var seededAt = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

// migrationWrites counts the successful write commands one run of migration 0036 sent, by
// collection.
type migrationWrites struct {
	mu      sync.Mutex
	pending map[int64]string // request id -> collection of a started write command
	byColl  map[string]int
}

func (w *migrationWrites) count(coll string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.byColl[coll]
}

// runNationalIDMigration invokes migration 0036's Up directly against the test database, through a
// client that counts the write commands it sends. ctx may carry a lock renewer
// (migrations.WithLockRenewer).
func runNationalIDMigration(ctx context.Context, c *qt.C) (*migrationWrites, error) {
	w := &migrationWrites{pending: map[int64]string{}, byColl: map[string]int{}}
	writeCommands := map[string]bool{"insert": true, "update": true, "delete": true, "findAndModify": true}
	monitor := &event.CommandMonitor{
		Started: func(_ context.Context, e *event.CommandStartedEvent) {
			if !writeCommands[e.CommandName] {
				return
			}
			coll, _ := e.Command.Lookup(e.CommandName).StringValueOK()
			w.mu.Lock()
			w.pending[e.RequestID] = coll
			w.mu.Unlock()
		},
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if coll, ok := w.pending[e.RequestID]; ok {
				w.byColl[coll]++
				delete(w.pending, e.RequestID)
			}
		},
	}
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI).SetMonitor(monitor))
	c.Assert(err, qt.IsNil)
	defer func() { c.Assert(client.Disconnect(context.Background()), qt.IsNil) }()
	mig := migrationByVersion(c, migrations.NationalIDNormalizationVersion)
	return w, mig.Up(ctx, client.Database(testDB.database))
}

// seedRawMember stores a member document directly, as any database before migration 36 holds it.
func seedRawMember(c *qt.C, nationalID string) OrgMember {
	id := bson.NewObjectID()
	m := OrgMember{
		ID: id, OrgAddress: testOrgAddress, NationalID: nationalID,
		Email: id.Hex() + "@example.com", MemberNumber: "M-" + id.Hex(), Weight: 1, CreatedAt: time.Now(),
	}
	_, err := testDB.orgMembers.InsertOne(context.Background(), m)
	c.Assert(err, qt.IsNil)
	return m
}

// seedCensus stores a census and returns it with its id set.
func seedCensus(c *qt.C, auth OrgMemberAuthFields, twoFa OrgMemberTwoFaFields) *Census {
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: twoFa, CreatedAt: time.Now()}
	id, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	census.ID, err = bson.ObjectIDFromHex(id)
	c.Assert(err, qt.IsNil)
	return census
}

// seedParticipant stores m as a participant of census under the given hash set.
func seedParticipant(c *qt.C, census *Census, m OrgMember, hashes map[string][]byte) {
	_, err := testDB.censusParticipants.InsertOne(context.Background(), &CensusParticipant{
		ParticipantID: m.ID.Hex(), CensusID: census.ID.Hex(),
		LoginHash: hashes["loginHash"], LoginHashEmail: hashes["loginHashEmail"], LoginHashPhone: hashes["loginHashPhone"],
		CreatedAt: seededAt, UpdatedAt: seededAt,
	})
	c.Assert(err, qt.IsNil)
}

// seedRawParticipant stores m as a participant of census hashed from its stored values verbatim,
// as every participant was hashed before migration 36.
func seedRawParticipant(c *qt.C, census *Census, m OrgMember) {
	seedParticipant(c, census, m, rawParticipantHashes(*census, m))
}

// rawDocs returns every document of coll as stored, keyed by key(doc).
func rawDocs(c *qt.C, coll *mongo.Collection, key func(bson.Raw) string) map[string]bson.Raw {
	cur, err := coll.Find(context.Background(), bson.M{})
	c.Assert(err, qt.IsNil)
	var docs []bson.Raw
	c.Assert(cur.All(context.Background(), &docs), qt.IsNil)
	out := make(map[string]bson.Raw, len(docs))
	for _, d := range docs {
		out[key(d)] = d
	}
	return out
}

// rawMembers returns every orgMembers document byte for byte, keyed by _id.
func rawMembers(c *qt.C) map[string]bson.Raw {
	return rawDocs(c, testDB.orgMembers, func(d bson.Raw) string { return d.Lookup("_id").ObjectID().Hex() })
}

// rawParticipants returns every censusParticipants document byte for byte, keyed as participantKey.
func rawParticipants(c *qt.C) map[string]bson.Raw {
	return rawDocs(c, testDB.censusParticipants, func(d bson.Raw) string {
		return d.Lookup("participantID").StringValue() + "/" + d.Lookup("censusId").StringValue()
	})
}

func participantKey(census *Census, m OrgMember) string {
	return m.ID.Hex() + "/" + census.ID.Hex()
}

func storedParticipant(c *qt.C, census *Census, m OrgMember) CensusParticipant {
	var p CensusParticipant
	c.Assert(testDB.censusParticipants.FindOne(context.Background(),
		bson.M{"participantID": m.ID.Hex(), "censusId": census.ID.Hex()}).Decode(&p), qt.IsNil)
	return p
}

// assertCanonicalHashes checks that m's participant in census holds exactly the hashes the db layer
// computes for it, i.e. the canonical ones, written by the migration.
func assertCanonicalHashes(c *qt.C, census *Census, m OrgMember) {
	p := storedParticipant(c, census, m)
	want := calculateParticipantHashes(*census, m)
	comment := qt.Commentf("member %s", m.ID.Hex())
	c.Assert(p.LoginHash, qt.DeepEquals, want["loginHash"], comment)
	c.Assert(p.LoginHashEmail, qt.DeepEquals, want["loginHashEmail"], comment)
	c.Assert(p.LoginHashPhone, qt.DeepEquals, want["loginHashPhone"], comment)
	c.Assert(p.UpdatedAt.After(seededAt), qt.IsTrue, comment)
}

// TestMigration0036NormalizeNationalIDs covers AC6: participants of variant-stored members get the
// canonical login hashes, the member documents are never written, and a re-run writes nothing.
func TestMigration0036NormalizeNationalIDs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	byID := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
	byID2FA := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID},
		OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone})
	byNumber := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber}, OrgMemberTwoFaFields{})

	separators := seedRawMember(c, "2.345.678-t")
	letterless := seedRawMember(c, "12345678")
	padding := seedRawMember(c, "345678B") // canonical 00345678B
	caseOnly := seedRawMember(c, "x1234567l")
	wrongLetter := seedRawMember(c, "12345678A")
	passport := seedRawMember(c, "PAB 123456")
	canonical := seedRawMember(c, "87654321X")
	// a variant whose participants already hold the canonical hashes (an interrupted earlier run, or
	// a participant written by the new code): the per-participant no-op guard leaves them alone
	resumed := seedRawMember(c, "23.456.789")
	for _, m := range []OrgMember{separators, letterless, padding, caseOnly, wrongLetter, passport, canonical} {
		seedRawParticipant(c, byID, m)
	}
	seedRawParticipant(c, byID2FA, separators)
	seedRawParticipant(c, byNumber, separators)
	// absent and empty hash keys are equal to the guard: here loginHashEmail/loginHashPhone are stored
	// as empty binaries, where the recomputed set has no such key
	seedParticipant(c, byID, resumed, map[string][]byte{
		"loginHash":      calculateParticipantHashes(*byID, resumed)["loginHash"],
		"loginHashEmail": {}, "loginHashPhone": {},
	})
	// and here loginHashPhone is missing from the document altogether
	resumed2FA := calculateParticipantHashes(*byID2FA, resumed)
	_, err := testDB.censusParticipants.InsertOne(context.Background(), bson.M{
		"participantID": resumed.ID.Hex(), "censusId": byID2FA.ID.Hex(),
		"loginHash": resumed2FA["loginHash"], "loginHashEmail": resumed2FA["loginHashEmail"],
		"createdAt": seededAt, "updatedAt": seededAt,
	})
	c.Assert(err, qt.IsNil)

	membersBefore := rawMembers(c)
	participantsBefore := rawParticipants(c)

	writes, err := runNationalIDMigration(context.Background(), c)
	c.Assert(err, qt.IsNil)

	// orgMembers is read-only: not one write command, every document byte-identical
	c.Assert(writes.count("orgMembers"), qt.Equals, 0)
	c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)

	// exactly the participants of variant members in nationalId censuses are rewritten, once each
	rehashed := []struct {
		census *Census
		member OrgMember
	}{{byID, separators}, {byID2FA, separators}, {byID, letterless}, {byID, padding}}
	c.Assert(writes.count("censusParticipants"), qt.Equals, len(rehashed))
	participantsAfter := rawParticipants(c)
	c.Assert(participantsAfter, qt.HasLen, len(participantsBefore))
	for _, p := range rehashed {
		assertCanonicalHashes(c, p.census, p.member)
		delete(participantsBefore, participantKey(p.census, p.member))
	}
	// case-only, non-canonicalisable, canonical, already-canonical-hashed and non-nationalId-census
	// participants are untouched
	for key, before := range participantsBefore {
		c.Assert(participantsAfter[key], qt.DeepEquals, before, qt.Commentf("participant %s", key))
	}

	// the real login lookup resolves every member from its variants
	for _, tc := range []struct {
		census *Census
		member OrgMember
		typed  []string
	}{
		{byID, separators, []string{"2.345.678-t", "02345678T", "2345678"}},
		{byID2FA, separators, []string{"2.345.678-t", "02345678t"}},
		{byID, letterless, []string{"12345678", "12345678Z", "12.345.678-z"}},
		{byID, padding, []string{"345678B", "00345678B", "345678"}},
		{byID, caseOnly, []string{"X1234567L", "x-1234567-l"}},
		{byID, wrongLetter, []string{"12345678a"}},
		{byID, passport, []string{"PAB 123456"}},
		{byID, canonical, []string{"87654321X", "87654321"}},
		{byID, resumed, []string{"23.456.789", "23456789D"}},
		{byID2FA, resumed, []string{"23456789"}},
	} {
		for _, typed := range tc.typed {
			input := (&OrgMember{NationalID: typed, Email: tc.member.Email}).Normalized()
			found, err := testDB.CensusParticipantByLoginHash(*tc.census, *input)
			c.Assert(err, qt.IsNil, qt.Commentf("member %s typed %q", tc.member.ID.Hex(), typed))
			c.Assert(found.ParticipantID, qt.Equals, tc.member.ID.Hex())
		}
	}

	// idempotent: a second run writes nothing at all
	writes, err = runNationalIDMigration(context.Background(), c)
	c.Assert(err, qt.IsNil)
	c.Assert(writes.count("orgMembers"), qt.Equals, 0)
	c.Assert(writes.count("censusParticipants"), qt.Equals, 0)
	c.Assert(rawParticipants(c), qt.DeepEquals, participantsAfter)
	c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)
}

// TestMigration0036Collision covers the migration half of AC7: a participant whose canonical hash
// another participant of the census holds is skipped (keeping its raw hash), the same member's
// participants in other censuses are still rehashed, and validation blocks the pair as duplicates.
func TestMigration0036Collision(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	reset := func(c *qt.C) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	}
	nationalIDCensus := func(c *qt.C) *Census {
		return seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{})
	}

	c.Run("the letterless twin is skipped in the shared census only", func(c *qt.C) {
		reset(c)
		shared := nationalIDCensus(c)
		other := nationalIDCensus(c)
		letterless := seedRawMember(c, "12345678")
		lettered := seedRawMember(c, "12345678Z")
		seedRawParticipant(c, shared, letterless)
		seedRawParticipant(c, shared, lettered)
		seedRawParticipant(c, other, letterless)
		membersBefore := rawMembers(c)
		participantsBefore := rawParticipants(c)

		writes, err := runNationalIDMigration(context.Background(), c)
		c.Assert(err, qt.IsNil)

		c.Assert(writes.count("orgMembers"), qt.Equals, 0)
		c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)
		// the shared census is untouched: the letterless participant keeps its raw hash
		c.Assert(writes.count("censusParticipants"), qt.Equals, 1)
		participantsAfter := rawParticipants(c)
		for _, m := range []OrgMember{letterless, lettered} {
			key := participantKey(shared, m)
			c.Assert(participantsAfter[key], qt.DeepEquals, participantsBefore[key], qt.Commentf("participant %s", key))
		}
		// the per-participant unit: the same member is still rehashed where nothing collides
		assertCanonicalHashes(c, other, letterless)

		// each resolves as themselves, the survivor only through its exact stored value
		found, err := testDB.CensusParticipantByVerbatimLoginHash(*shared, OrgMember{NationalID: "12345678"})
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, letterless.ID.Hex())
		for _, typed := range []string{"12345678Z", "12.345.678-z"} {
			found, err = testDB.CensusParticipantByLoginHash(*shared, OrgMember{NationalID: typed})
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, lettered.ID.Hex(), qt.Commentf("typed %q", typed))
		}
		for _, typed := range []string{"12345678", "12345678Z"} {
			found, err = testDB.CensusParticipantByLoginHash(*other, OrgMember{NationalID: typed})
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, letterless.ID.Hex(), qt.Commentf("typed %q", typed))
		}

		// validation blocks the pair as duplicates (R19) and warns about both
		results, err := testDB.CheckMembersFields(testOrgAddress, []string{letterless.ID.Hex(), lettered.ID.Hex()},
			shared.AuthFields, shared.TwoFaFields)
		c.Assert(err, qt.IsNil)
		c.Assert(results.Duplicates, qt.ContentEquals, []bson.ObjectID{letterless.ID, lettered.ID})
		var duplicates []NationalIDWarning
		for _, w := range results.NationalIDWarnings {
			if w.Class == NationalIDWarningDuplicate {
				duplicates = append(duplicates, w)
			}
		}
		c.Assert(duplicates, qt.ContentEquals, []NationalIDWarning{
			{MemberID: letterless.ID.Hex(), Class: NationalIDWarningDuplicate},
			{MemberID: lettered.ID.Hex(), Class: NationalIDWarningDuplicate},
		})

		// a re-run skips the survivor again and writes nothing
		writes, err = runNationalIDMigration(context.Background(), c)
		c.Assert(err, qt.IsNil)
		c.Assert(writes.count("orgMembers"), qt.Equals, 0)
		c.Assert(writes.count("censusParticipants"), qt.Equals, 0)
		c.Assert(rawParticipants(c), qt.DeepEquals, participantsAfter)
	})

	// both twins stored non-canonically: the first in _id order takes the canonical hash, whichever
	// variant it holds, and the other is skipped as the verbatim survivor (spec §5.3)
	for _, order := range [][2]string{{"12.345.678-z", "12345678"}, {"12345678", "12.345.678-z"}} {
		c.Run(fmt.Sprintf("%q before %q", order[0], order[1]), func(c *qt.C) {
			reset(c)
			census := nationalIDCensus(c)
			first := seedRawMember(c, order[0])
			second := seedRawMember(c, order[1])
			c.Assert(first.ID.Hex() < second.ID.Hex(), qt.IsTrue)
			seedRawParticipant(c, census, first)
			seedRawParticipant(c, census, second)
			membersBefore := rawMembers(c)
			secondBefore := rawParticipants(c)[participantKey(census, second)]

			writes, err := runNationalIDMigration(context.Background(), c)
			c.Assert(err, qt.IsNil)
			c.Assert(writes.count("orgMembers"), qt.Equals, 0)
			c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)
			c.Assert(writes.count("censusParticipants"), qt.Equals, 1)
			assertCanonicalHashes(c, census, first)
			c.Assert(rawParticipants(c)[participantKey(census, second)], qt.DeepEquals, secondBefore)

			found, err := testDB.CensusParticipantByVerbatimLoginHash(*census, OrgMember{NationalID: order[1]})
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, second.ID.Hex())
			found, err = testDB.CensusParticipantByLoginHash(*census, OrgMember{NationalID: "12345678Z"})
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, first.ID.Hex())
		})
	}
}

// TestMigration0036Batches covers AC15: more than one batch, one lock renewal per batch, every
// candidate's participant rehashed exactly once, no orgMembers write, and a lost lock aborting
// before its batch writes.
func TestMigration0036Batches(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	const candidates = 2*500 + 1 // nationalIDBatchSize is 500: two full batches and one more

	var census *Census
	var members []OrgMember
	seed := func(c *qt.C) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
		census = seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{})
		members = make([]OrgMember, candidates)
		memberDocs := make([]any, candidates)
		participantDocs := make([]any, candidates)
		for i := range members {
			members[i] = OrgMember{
				ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: fmt.Sprintf("%08d", 10000000+i),
				CreatedAt: time.Now(),
			}
			memberDocs[i] = members[i]
			participantDocs[i] = CensusParticipant{
				ParticipantID: members[i].ID.Hex(), CensusID: census.ID.Hex(),
				LoginHash: rawParticipantHashes(*census, members[i])["loginHash"], CreatedAt: seededAt, UpdatedAt: seededAt,
			}
		}
		_, err := testDB.orgMembers.InsertMany(context.Background(), memberDocs)
		c.Assert(err, qt.IsNil)
		_, err = testDB.censusParticipants.InsertMany(context.Background(), participantDocs)
		c.Assert(err, qt.IsNil)
	}
	// canonicalHashed counts the participants holding their canonical hash, each written by the run
	canonicalHashed := func(c *qt.C) int {
		n := 0
		for _, m := range members {
			p := storedParticipant(c, census, m)
			if string(p.LoginHash) == string(calculateParticipantHashes(*census, m)["loginHash"]) {
				c.Assert(p.UpdatedAt.After(seededAt), qt.IsTrue)
				n++
			}
		}
		return n
	}

	c.Run("every candidate is rehashed once, renewing the lock per batch", func(c *qt.C) {
		seed(c)
		membersBefore := rawMembers(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			return nil
		})
		writes, err := runNationalIDMigration(ctx, c)
		c.Assert(err, qt.IsNil)
		c.Assert(renewals, qt.Equals, 3)
		// one write per participant and every participant canonical: each was rehashed exactly once
		c.Assert(writes.count("censusParticipants"), qt.Equals, candidates)
		c.Assert(canonicalHashed(c), qt.Equals, candidates)
		c.Assert(writes.count("orgMembers"), qt.Equals, 0)
		c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)
	})

	c.Run("a lost lock aborts before the batch writes", func(c *qt.C) {
		seed(c)
		membersBefore := rawMembers(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			if renewals == 2 {
				return fmt.Errorf("migration lock lost: another instance holds it")
			}
			return nil
		})
		writes, err := runNationalIDMigration(ctx, c)
		c.Assert(err, qt.ErrorMatches, ".*migration lock lost.*")
		c.Assert(writes.count("censusParticipants"), qt.Equals, 500) // the first batch only
		c.Assert(canonicalHashed(c), qt.Equals, 500)
		c.Assert(writes.count("orgMembers"), qt.Equals, 0)
		c.Assert(rawMembers(c), qt.DeepEquals, membersBefore)
	})
}
