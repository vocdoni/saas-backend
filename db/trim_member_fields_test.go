package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// runTrimMemberFields invokes the whitespace backfill against the current test
// database state. It is a plain exported function rather than a registered
// migration, so it is called directly.
func runTrimMemberFields(t *testing.T, opts migrations.TrimOptions) migrations.TrimReport {
	t.Helper()
	report, err := migrations.TrimMemberFields(
		context.Background(), testDB.DBClient.Database(testDB.database), opts)
	qt.Assert(t, err, qt.IsNil)
	return report
}

// seedUntrimmedMember inserts a member directly, bypassing the write chokepoint
// that would now trim it, and stores the census participant carrying the
// whitespace-derived login hash. This reproduces a row written before the fix.
func seedUntrimmedMember(t *testing.T, census *Census, censusID string, member *OrgMember) []byte {
	t.Helper()
	ctx := context.Background()

	_, err := testDB.orgMembers.InsertOne(ctx, member)
	qt.Assert(t, err, qt.IsNil)

	staleHash := HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields)
	participant := &CensusParticipant{
		ParticipantID: member.ID.Hex(),
		CensusID:      censusID,
		LoginHash:     staleHash,
		CreatedAt:     time.Now(),
	}
	if len(census.TwoFaFields) == 2 {
		if member.Email != "" {
			participant.LoginHashEmail = HashAuthTwoFaFields(
				*member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
		}
		if !member.Phone.IsEmpty() {
			participant.LoginHashPhone = HashAuthTwoFaFields(
				*member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone})
		}
	}
	_, err = testDB.censusParticipants.InsertOne(ctx, participant)
	qt.Assert(t, err, qt.IsNil)
	return staleHash
}

func TestTrimMemberFields(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	org := &Organization{Address: testOrgAddress, Active: true, CreatedAt: time.Now()}

	// newAuthCensus creates an auth-only census keyed on name+surname+memberNumber,
	// the shape used by the censuses this fix repairs.
	newAuthCensus := func() (*Census, string) {
		census := &Census{
			OrgAddress: testOrgAddress,
			AuthFields: OrgMemberAuthFields{
				OrgMemberAuthFieldsName,
				OrgMemberAuthFieldsSurname,
				OrgMemberAuthFieldsMemberNumber,
			},
			TwoFaFields: OrgMemberTwoFaFields{},
			CreatedAt:   time.Now(),
		}
		id, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		census.ID, err = primitive.ObjectIDFromHex(id)
		c.Assert(err, qt.IsNil)
		return census, id
	}

	reset := func() {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(org), qt.IsNil)
	}

	t.Run("trims fields and repairs the login hash", func(_ *testing.T) {
		reset()
		census, censusID := newAuthCensus()

		member := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			Name:         "John ",
			Surname:      " Doe",
			MemberNumber: "M-001 ",
			CreatedAt:    time.Now(),
		}
		staleHash := seedUntrimmedMember(t, census, censusID, member)

		// Before the repair, the clean values a voter would type do not match.
		clean := OrgMember{
			OrgAddress: testOrgAddress, Name: "John", Surname: "Doe", MemberNumber: "M-001",
		}
		_, err := testDB.CensusParticipantByLoginHash(*census, clean)
		c.Assert(err, qt.Equals, ErrNotFound)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(report.Trimmed, qt.Equals, 1)
		c.Assert(report.ParticipantsUpdated, qt.Equals, 1)
		c.Assert(report.CensusesAffected, qt.Equals, 1)
		c.Assert(report.Skipped, qt.Equals, 0)

		// The member fields are trimmed.
		ctx := context.Background()
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": member.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Name, qt.Equals, "John")
		c.Assert(got.Surname, qt.Equals, "Doe")
		c.Assert(got.MemberNumber, qt.Equals, "M-001")

		// The participant hash is the canonical hash of the trimmed member.
		expected := HashAuthTwoFaFields(got, census.AuthFields, census.TwoFaFields)
		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals, expected)
		c.Assert(gotParticipant.LoginHash, qt.Not(qt.DeepEquals), staleHash)

		// End to end: the clean values now find the participant.
		found, err := testDB.CensusParticipantByLoginHash(*census, clean)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
	})

	t.Run("recomputes the email and phone hashes of a two-factor census", func(_ *testing.T) {
		reset()

		// A census with both 2FA fields stores loginHashEmail and loginHashPhone
		// alongside loginHash, so all three have to be recomputed.
		census := &Census{
			OrgAddress: testOrgAddress,
			AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname},
			TwoFaFields: OrgMemberTwoFaFields{
				OrgMemberTwoFaFieldEmail,
				OrgMemberTwoFaFieldPhone,
			},
			CreatedAt: time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		census.ID, err = primitive.ObjectIDFromHex(censusID)
		c.Assert(err, qt.IsNil)

		phone, err := NewHashedPhone(testPlaintextPhone, org)
		c.Assert(err, qt.IsNil)
		member := &OrgMember{
			ID:         primitive.NewObjectID(),
			OrgAddress: testOrgAddress,
			Name:       "Jane  ",
			Surname:    "Roe",
			Email:      "jane.roe@example.com",
			Phone:      phone,
			CreatedAt:  time.Now(),
		}
		seedUntrimmedMember(t, census, censusID, member)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(report.Trimmed, qt.Equals, 1)

		ctx := context.Background()
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": member.ID}).Decode(&got), qt.IsNil)

		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals,
			HashAuthTwoFaFields(got, census.AuthFields, census.TwoFaFields))
		c.Assert(gotParticipant.LoginHashEmail, qt.DeepEquals,
			HashAuthTwoFaFields(got, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}))
		c.Assert(gotParticipant.LoginHashPhone, qt.DeepEquals,
			HashAuthTwoFaFields(got, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone}))

		// Logging in with either second factor resolves to this member.
		byEmail := OrgMember{
			OrgAddress: testOrgAddress, Name: "Jane", Surname: "Roe", Email: "jane.roe@example.com",
		}
		found, err := testDB.CensusParticipantByLoginHash(*census, byEmail)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
	})

	t.Run("dry run reports without writing", func(_ *testing.T) {
		reset()
		census, censusID := newAuthCensus()

		member := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			Name:         "John ",
			Surname:      "Doe",
			MemberNumber: "M-002",
			CreatedAt:    time.Now(),
		}
		staleHash := seedUntrimmedMember(t, census, censusID, member)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: false})
		c.Assert(report.Trimmed, qt.Equals, 1)
		c.Assert(report.ParticipantsUpdated, qt.Equals, 1)

		// Nothing was written.
		ctx := context.Background()
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": member.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Name, qt.Equals, "John ")
		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals, staleHash)
	})

	t.Run("skips members whose trimmed hash would collide", func(_ *testing.T) {
		reset()
		census, censusID := newAuthCensus()

		// Already clean, and therefore the hash the padded member would collide with.
		clean := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			Name:         "John",
			Surname:      "Doe",
			MemberNumber: "M-003",
			CreatedAt:    time.Now(),
		}
		seedUntrimmedMember(t, census, censusID, clean)

		// Same values, but padded: trimming it would produce the hash above.
		padded := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			Name:         "John ",
			Surname:      "Doe",
			MemberNumber: "M-003",
			CreatedAt:    time.Now(),
		}
		paddedHash := seedUntrimmedMember(t, census, censusID, padded)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(report.Skipped, qt.Equals, 1)
		c.Assert(report.Trimmed, qt.Equals, 0)
		c.Assert(report.SkippedMembers, qt.HasLen, 1)
		c.Assert(report.SkippedMembers[0].MemberID, qt.Equals, padded.ID.Hex())
		c.Assert(report.SkippedMembers[0].CensusID, qt.Equals, censusID)

		// The padded member and its hash are left exactly as they were.
		ctx := context.Background()
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": padded.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Name, qt.Equals, "John ")
		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": padded.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals, paddedHash)

		// The clean member still authenticates.
		found, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{
			OrgAddress: testOrgAddress, Name: "John", Surname: "Doe", MemberNumber: "M-003",
		})
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, clean.ID.Hex())
	})

	t.Run("is idempotent", func(_ *testing.T) {
		reset()
		census, censusID := newAuthCensus()

		member := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			Name:         " John",
			Surname:      "Doe ",
			MemberNumber: "M-004",
			CreatedAt:    time.Now(),
		}
		seedUntrimmedMember(t, census, censusID, member)

		first := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(first.Trimmed, qt.Equals, 1)

		second := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(second.Scanned, qt.Equals, 0)
		c.Assert(second.Trimmed, qt.Equals, 0)
		c.Assert(second.ParticipantsUpdated, qt.Equals, 0)
	})

	t.Run("restricts the run to the selected organization", func(_ *testing.T) {
		reset()
		other := &Organization{Address: testAnotherOrgAddress, Active: true, CreatedAt: time.Now()}
		c.Assert(testDB.SetOrganization(other), qt.IsNil)
		census, censusID := newAuthCensus()

		mine := &OrgMember{
			ID: primitive.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "John ", Surname: "Doe", MemberNumber: "M-005", CreatedAt: time.Now(),
		}
		seedUntrimmedMember(t, census, censusID, mine)

		theirs := &OrgMember{
			ID: primitive.NewObjectID(), OrgAddress: testAnotherOrgAddress,
			Name: "Jane ", Surname: "Roe", MemberNumber: "M-006", CreatedAt: time.Now(),
		}
		_, err := testDB.orgMembers.InsertOne(context.Background(), theirs)
		c.Assert(err, qt.IsNil)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: true, OrgAddress: &testOrgAddress})
		c.Assert(report.Scanned, qt.Equals, 1)
		c.Assert(report.Trimmed, qt.Equals, 1)

		// The other organization's member is untouched.
		ctx := context.Background()
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": theirs.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Name, qt.Equals, "Jane ")
	})

	t.Run("leaves participants of a deleted census alone", func(_ *testing.T) {
		reset()
		census, censusID := newAuthCensus()

		member := &OrgMember{
			ID: primitive.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "John ", Surname: "Doe", MemberNumber: "M-007", CreatedAt: time.Now(),
		}
		staleHash := seedUntrimmedMember(t, census, censusID, member)

		// Drop the census, orphaning the participant.
		ctx := context.Background()
		_, err := testDB.censuses.DeleteOne(ctx, bson.M{"_id": census.ID})
		c.Assert(err, qt.IsNil)

		report := runTrimMemberFields(t, migrations.TrimOptions{Apply: true})
		c.Assert(report.Trimmed, qt.Equals, 1)
		c.Assert(report.OrphanParticipants, qt.Equals, 1)
		c.Assert(report.ParticipantsUpdated, qt.Equals, 0)

		// The member is repaired but the orphaned hash is left as it was.
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": member.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Name, qt.Equals, "John")
		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals, staleHash)
	})
}
