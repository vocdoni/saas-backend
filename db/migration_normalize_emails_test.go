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

// runNormalizeEmailsMigration invokes migration 0015's Up function directly
// against the current test database state.
func runNormalizeEmailsMigration(t *testing.T) {
	t.Helper()
	mig, ok := migrations.AsMap()[15]
	qt.Assert(t, ok, qt.IsTrue)
	err := mig.Up(context.Background(), testDB.DBClient.Database(testDB.database))
	qt.Assert(t, err, qt.IsNil)
}

func TestNormalizeMemberEmailsMigration(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	org := &Organization{Address: testOrgAddress, Active: true, CreatedAt: time.Now()}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	t.Run("normalizes email and recomputes login hashes", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(org), qt.IsNil)

		// A mail census whose login hash includes the email.
		census := &Census{
			OrgAddress:  testOrgAddress,
			AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber},
			TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
			CreatedAt:   time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)

		// Seed a member with an uppercase email directly, bypassing the write
		// chokepoint that would normalize it (simulating pre-existing data).
		member := &OrgMember{
			ID:           primitive.NewObjectID(),
			OrgAddress:   testOrgAddress,
			MemberNumber: "M-1",
			Email:        "User@Example.COM",
			CreatedAt:    time.Now(),
		}
		ctx := context.Background()
		_, err = testDB.orgMembers.InsertOne(ctx, member)
		c.Assert(err, qt.IsNil)

		// Seed the participant with the uppercase-derived login hash.
		upperHash := HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields)
		participant := &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      censusID,
			LoginHash:     upperHash,
			CreatedAt:     time.Now(),
		}
		_, err = testDB.censusParticipants.InsertOne(ctx, participant)
		c.Assert(err, qt.IsNil)

		runNormalizeEmailsMigration(t)

		// Email is now lowercase.
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": member.ID}).Decode(&got), qt.IsNil)
		c.Assert(got.Email, qt.Equals, "user@example.com")

		// Login hash matches the canonical hash of the lowercased member.
		lowered := *member
		lowered.Email = "user@example.com"
		expected := HashAuthTwoFaFields(lowered, census.AuthFields, census.TwoFaFields)
		var gotParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&gotParticipant), qt.IsNil)
		c.Assert(gotParticipant.LoginHash, qt.DeepEquals, expected)
		c.Assert(gotParticipant.LoginHash, qt.Not(qt.DeepEquals), upperHash)

		// End-to-end: the participant is now found using a lowercase login input.
		census.ID, err = primitive.ObjectIDFromHex(censusID)
		c.Assert(err, qt.IsNil)
		input := OrgMember{OrgAddress: testOrgAddress, MemberNumber: "M-1", Email: "user@example.com"}
		found, err := testDB.CensusParticipantByLoginHash(*census, input)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
	})

	t.Run("skips members that would collide after lowercasing", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(org), qt.IsNil)

		// Mail census with no auth fields: the login hash is derived solely from
		// the email, so two emails differing only by case collide once lowered.
		census := &Census{
			OrgAddress:  testOrgAddress,
			TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
			CreatedAt:   time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		ctx := context.Background()

		// Member already stored in lowercase form.
		lowerMember := &OrgMember{
			ID: primitive.NewObjectID(), OrgAddress: testOrgAddress, Email: "clash@example.com", CreatedAt: time.Now(),
		}
		_, err = testDB.orgMembers.InsertOne(ctx, lowerMember)
		c.Assert(err, qt.IsNil)
		lowerHash := HashAuthTwoFaFields(*lowerMember, census.AuthFields, census.TwoFaFields)
		_, err = testDB.censusParticipants.InsertOne(ctx, &CensusParticipant{
			ParticipantID: lowerMember.ID.Hex(), CensusID: censusID, LoginHash: lowerHash, CreatedAt: time.Now(),
		})
		c.Assert(err, qt.IsNil)

		// Member stored with an uppercase variant of the same email.
		upperMember := &OrgMember{
			ID: primitive.NewObjectID(), OrgAddress: testOrgAddress, Email: "Clash@example.com", CreatedAt: time.Now(),
		}
		_, err = testDB.orgMembers.InsertOne(ctx, upperMember)
		c.Assert(err, qt.IsNil)
		upperHash := HashAuthTwoFaFields(*upperMember, census.AuthFields, census.TwoFaFields)
		_, err = testDB.censusParticipants.InsertOne(ctx, &CensusParticipant{
			ParticipantID: upperMember.ID.Hex(), CensusID: censusID, LoginHash: upperHash, CreatedAt: time.Now(),
		})
		c.Assert(err, qt.IsNil)

		runNormalizeEmailsMigration(t)

		// The uppercase member is left untouched (email and hash unchanged).
		var gotUpper OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": upperMember.ID}).Decode(&gotUpper), qt.IsNil)
		c.Assert(gotUpper.Email, qt.Equals, "Clash@example.com")
		var gotUpperParticipant CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": upperMember.ID.Hex(), "censusId": censusID}).Decode(&gotUpperParticipant), qt.IsNil)
		c.Assert(gotUpperParticipant.LoginHash, qt.DeepEquals, upperHash)

		// The already-lowercase member is untouched too.
		var gotLower OrgMember
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": lowerMember.ID}).Decode(&gotLower), qt.IsNil)
		c.Assert(gotLower.Email, qt.Equals, "clash@example.com")
	})
}
