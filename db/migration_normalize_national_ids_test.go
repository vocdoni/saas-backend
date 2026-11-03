package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// runNationalIDMigration invokes migration 0034's Up directly against the test database. ctx may
// carry a lock renewer (migrations.WithLockRenewer).
func runNationalIDMigration(ctx context.Context, c *qt.C) error {
	mig := migrationByVersion(c, migrations.NationalIDNormalizationVersion)
	return mig.Up(ctx, testDB.DBClient.Database(testDB.database))
}

// seedRawMember stores a member the way a database written before migration 34 holds it,
// bypassing the write path that would canonicalize its nationalId.
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

// seedParticipant stores m as a participant of census, hashed from its stored values.
func seedParticipant(c *qt.C, census *Census, m OrgMember) {
	_, err := testDB.censusParticipants.InsertOne(context.Background(), &CensusParticipant{
		ParticipantID: m.ID.Hex(), CensusID: census.ID.Hex(),
		LoginHash: HashAuthTwoFaFields(m, census.AuthFields, census.TwoFaFields), CreatedAt: time.Now(),
	})
	c.Assert(err, qt.IsNil)
}

func storedMember(c *qt.C, id bson.ObjectID) OrgMember {
	var m OrgMember
	c.Assert(testDB.orgMembers.FindOne(context.Background(), bson.M{"_id": id}).Decode(&m), qt.IsNil)
	return m
}

func storedLoginHash(c *qt.C, census *Census, m OrgMember) []byte {
	var p CensusParticipant
	c.Assert(testDB.censusParticipants.FindOne(context.Background(),
		bson.M{"participantID": m.ID.Hex(), "censusId": census.ID.Hex()}).Decode(&p), qt.IsNil)
	return p.LoginHash
}

func TestMigration0034NormalizeNationalIDs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	byID := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
	byNumber := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber}, OrgMemberTwoFaFields{})

	separators := seedRawMember(c, "2.345.678-t")
	letterless := seedRawMember(c, "12345678")
	wrongLetter := seedRawMember(c, "12345678A")
	passport := seedRawMember(c, "PAB 123456")
	canonical := seedRawMember(c, "X1234567L")
	for _, m := range []OrgMember{separators, letterless, wrongLetter, passport, canonical} {
		seedParticipant(c, byID, m)
	}
	seedParticipant(c, byNumber, separators)
	byNumberHash := storedLoginHash(c, byNumber, separators)

	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)

	for _, tc := range []struct {
		member           OrgMember
		stored, original string
		typed            string
	}{
		{separators, "02345678T", "", "02345678T"},
		{letterless, "12345678Z", "12345678", "12.345.678-z"},
		{wrongLetter, "12345678A", "", "12345678a"},
		{passport, "PAB 123456", "", "PAB 123456"},
		{canonical, "X1234567L", "", "x-1234567-l"},
	} {
		got := storedMember(c, tc.member.ID)
		c.Assert(got.NationalID, qt.Equals, tc.stored)
		c.Assert(got.NationalIDOriginal, qt.Equals, tc.original)
		// the stored hash is the canonical one, and the real login lookup resolves any variant
		c.Assert(storedLoginHash(c, byID, got), qt.DeepEquals, HashAuthTwoFaFields(got, byID.AuthFields, byID.TwoFaFields))
		input := (&OrgMember{NationalID: tc.typed, Email: got.Email}).Normalized()
		found, err := testDB.CensusParticipantByLoginHash(*byID, *input)
		c.Assert(err, qt.IsNil, qt.Commentf("typed %q", tc.typed))
		c.Assert(found.ParticipantID, qt.Equals, got.ID.Hex())
	}
	// a census not logging in with the nationalId is not rewritten
	c.Assert(storedLoginHash(c, byNumber, separators), qt.DeepEquals, byNumberHash)

	// idempotent: a second run finds nothing to do
	before := storedMember(c, letterless.ID)
	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)
	c.Assert(storedMember(c, letterless.ID), qt.DeepEquals, before)
}

func TestMigration0034Collision(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	census := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{})
	letterless := seedRawMember(c, "12345678")
	lettered := seedRawMember(c, "12345678Z")
	seedParticipant(c, census, letterless)
	seedParticipant(c, census, lettered)
	letterlessHash := storedLoginHash(c, census, letterless)

	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)

	// the letterless member is skipped whole: value, marker and hash as before
	got := storedMember(c, letterless.ID)
	c.Assert(got.NationalID, qt.Equals, "12345678")
	c.Assert(got.NationalIDOriginal, qt.Equals, "")
	c.Assert(storedLoginHash(c, census, got), qt.DeepEquals, letterlessHash)
	c.Assert(storedMember(c, lettered.ID).NationalID, qt.Equals, "12345678Z")

	// each still resolves from their own stored value
	found, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{NationalID: "12345678"})
	c.Assert(err, qt.IsNil)
	c.Assert(found.ParticipantID, qt.Equals, letterless.ID.Hex())

	// validation reports the pair, without blocking on it (their hashes differ)
	results, err := testDB.CheckMembersFields(testOrgAddress, []string{letterless.ID.Hex(), lettered.ID.Hex()},
		census.AuthFields, census.TwoFaFields)
	c.Assert(err, qt.IsNil)
	c.Assert(results.Duplicates, qt.HasLen, 0)
	c.Assert(results.NationalIDWarnings, qt.ContentEquals, []NationalIDWarning{
		{MemberID: letterless.ID.Hex(), Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: letterless.ID.Hex(), Class: NationalIDWarningDuplicate},
		{MemberID: lettered.ID.Hex(), Class: NationalIDWarningDuplicate},
	})
}

func TestMigration0034Batches(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	const candidates = 2*500 + 1 // nationalIDBatchSize is 500: two full batches and one more

	seed := func(c *qt.C) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		docs := make([]any, candidates)
		for i := range docs {
			docs[i] = OrgMember{
				ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: fmt.Sprintf("%08d", 10000000+i),
				CreatedAt: time.Now(),
			}
		}
		_, err := testDB.orgMembers.InsertMany(context.Background(), docs)
		c.Assert(err, qt.IsNil)
	}
	derived := func(c *qt.C) int64 {
		n, err := testDB.orgMembers.CountDocuments(context.Background(), bson.M{"nationalIdOriginal": bson.M{"$exists": true}})
		c.Assert(err, qt.IsNil)
		return n
	}

	c.Run("every candidate is normalized once, renewing the lock per batch", func(c *qt.C) {
		seed(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			return nil
		})
		c.Assert(runNationalIDMigration(ctx, c), qt.IsNil)
		c.Assert(renewals, qt.Equals, 3)
		c.Assert(derived(c), qt.Equals, int64(candidates))
		left, err := testDB.orgMembers.CountDocuments(context.Background(),
			bson.M{"nationalId": bson.M{"$not": bson.Regex{Pattern: "^[0-9]{8}[A-Z]$"}}})
		c.Assert(err, qt.IsNil)
		c.Assert(left, qt.Equals, int64(0))
	})

	c.Run("a lost lock aborts before the batch writes", func(c *qt.C) {
		seed(c)
		renewals := 0
		ctx := migrations.WithLockRenewer(context.Background(), func(context.Context) error {
			renewals++
			if renewals == 2 {
				return fmt.Errorf("migration lock lost: another instance holds it")
			}
			return nil
		})
		c.Assert(runNationalIDMigration(ctx, c), qt.ErrorMatches, ".*migration lock lost.*")
		c.Assert(derived(c), qt.Equals, int64(500)) // the first batch only
	})
}

// TestMigration0034ResumesInterruptedMember covers a run that crashed between the participant and
// the member writes: the member still holds its old value, its participant the canonical hash.
func TestMigration0034ResumesInterruptedMember(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	census := seedCensus(c, OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{})
	interrupted := seedRawMember(c, "12345678")
	rehashed := interrupted
	rehashed.NationalID = "12345678Z"
	seedParticipant(c, census, rehashed)

	c.Assert(runNationalIDMigration(context.Background(), c), qt.IsNil)

	got := storedMember(c, interrupted.ID)
	c.Assert(got.NationalID, qt.Equals, "12345678Z")
	c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")
	c.Assert(storedLoginHash(c, census, got), qt.DeepEquals, HashAuthTwoFaFields(got, census.AuthFields, census.TwoFaFields))
}
