package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// runRepair invokes the login-hash repair against the current test database
// state. It is a plain exported function rather than a registered migration, so
// it is called directly.
func runRepair(t *testing.T, opts migrations.RepairOptions) migrations.RepairReport {
	t.Helper()
	report, err := migrations.RepairLoginHashes(
		context.Background(), testDB.DBClient.Database(testDB.database), opts)
	qt.Assert(t, err, qt.IsNil)
	return report
}

// unfoldedHash computes what a login hash looked like before the values feeding
// it were folded to lowercase, i.e. straight from the exact-case member values.
// Used to seed rows as they exist in a database written by the old code.
func unfoldedHash(m OrgMember, auth OrgMemberAuthFields, twoFa OrgMemberTwoFaFields) []byte {
	values := make([]string, 0, len(auth)+len(twoFa))
	for _, f := range auth {
		switch f {
		case OrgMemberAuthFieldsName:
			values = append(values, m.Name)
		case OrgMemberAuthFieldsSurname:
			values = append(values, m.Surname)
		case OrgMemberAuthFieldsMemberNumber:
			values = append(values, m.MemberNumber)
		case OrgMemberAuthFieldsNationalID:
			values = append(values, m.NationalID)
		case OrgMemberAuthFieldsBirthDate:
			values = append(values, m.BirthDate)
		default:
			// mirrors HashAuthTwoFaFields: unknown fields are ignored
		}
	}
	for _, f := range twoFa {
		switch f {
		case OrgMemberTwoFaFieldEmail:
			values = append(values, m.Email)
		case OrgMemberTwoFaFieldPhone:
			if !m.Phone.IsEmpty() {
				values = append(values, string(m.Phone))
			}
		default:
			// mirrors HashAuthTwoFaFields: unknown fields are ignored
		}
	}
	return internal.HashSortedFields(values)
}

// seedLegacyParticipant inserts a member and its census participant directly,
// bypassing the write chokepoint, storing the hash the caller specifies. This
// reproduces a row written before the current hash rules.
func seedLegacyParticipant(t *testing.T, censusID string, member *OrgMember, legacy []byte) {
	t.Helper()
	ctx := context.Background()
	_, err := testDB.orgMembers.InsertOne(ctx, member)
	qt.Assert(t, err, qt.IsNil)
	_, err = testDB.censusParticipants.InsertOne(ctx, &CensusParticipant{
		ParticipantID: member.ID.Hex(),
		CensusID:      censusID,
		LoginHash:     legacy,
		CreatedAt:     time.Now(),
	})
	qt.Assert(t, err, qt.IsNil)
}

func TestRepairLoginHashes(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	org := &Organization{Address: testOrgAddress, CreatedAt: time.Now(), Country: "ES"}

	newCensus := func(auth OrgMemberAuthFields, twoFa OrgMemberTwoFaFields) (*Census, string) {
		census := &Census{
			OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: twoFa, CreatedAt: time.Now(),
		}
		id, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		census.ID, err = bson.ObjectIDFromHex(id)
		c.Assert(err, qt.IsNil)
		return census, id
	}
	authFields := OrgMemberAuthFields{
		OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname, OrgMemberAuthFieldsMemberNumber,
	}
	noTwoFa := OrgMemberTwoFaFields{}

	reset := func() {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(org), qt.IsNil)
	}
	storedParticipant := func(memberID, censusID string) CensusParticipant {
		var got CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(context.Background(),
			bson.M{"participantID": memberID, "censusId": censusID}).Decode(&got), qt.IsNil)
		return got
	}
	storedMember := func(id bson.ObjectID) OrgMember {
		var got OrgMember
		c.Assert(testDB.orgMembers.FindOne(context.Background(), bson.M{"_id": id}).Decode(&got), qt.IsNil)
		return got
	}

	t.Run("rehashes a legacy exact-case hash and keeps member casing", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "John", Surname: "Doe", MemberNumber: "M-001", CreatedAt: time.Now(),
		}
		seedLegacyParticipant(t, censusID, member,
			unfoldedHash(*member, census.AuthFields, census.TwoFaFields))

		// Before the repair the stored hash is exact-case while the service folds,
		// so nothing matches.
		_, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{
			OrgAddress: testOrgAddress, Name: "John", Surname: "Doe", MemberNumber: "M-001",
		})
		c.Assert(err, qt.Equals, ErrNotFound)

		report := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(report.ParticipantsRehashed, qt.Equals, 1)
		c.Assert(report.ParticipantsSkipped, qt.Equals, 0)
		c.Assert(report.CensusesAffected, qt.Equals, 1)

		c.Assert(storedParticipant(member.ID.Hex(), censusID).LoginHash, qt.DeepEquals,
			HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields))

		// The member document keeps the casing it was imported with.
		got := storedMember(member.ID)
		c.Assert(got.Name, qt.Equals, "John")
		c.Assert(got.Surname, qt.Equals, "Doe")
		c.Assert(got.MemberNumber, qt.Equals, "M-001")

		// Login now resolves in any casing.
		for _, in := range []OrgMember{
			{OrgAddress: testOrgAddress, Name: "John", Surname: "Doe", MemberNumber: "M-001"},
			{OrgAddress: testOrgAddress, Name: "john", Surname: "doe", MemberNumber: "m-001"},
			{OrgAddress: testOrgAddress, Name: "JOHN", Surname: "DOE", MemberNumber: "M-001"},
		} {
			found, err := testDB.CensusParticipantByLoginHash(*census, in)
			c.Assert(err, qt.IsNil)
			c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
		}
	})

	t.Run("trims whitespace and rehashes in one run", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Jane ", Surname: " Roe", MemberNumber: "M-002 ", CreatedAt: time.Now(),
		}
		seedLegacyParticipant(t, censusID, member,
			unfoldedHash(*member, census.AuthFields, census.TwoFaFields))

		report := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(report.MembersTrimmed, qt.Equals, 1)
		c.Assert(report.ParticipantsRehashed, qt.Equals, 1)

		got := storedMember(member.ID)
		c.Assert(got.Name, qt.Equals, "Jane")
		c.Assert(got.Surname, qt.Equals, "Roe")
		c.Assert(got.MemberNumber, qt.Equals, "M-002")

		c.Assert(storedParticipant(member.ID.Hex(), censusID).LoginHash, qt.DeepEquals,
			HashAuthTwoFaFields(got, census.AuthFields, census.TwoFaFields))

		found, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{
			OrgAddress: testOrgAddress, Name: "jane", Surname: "roe", MemberNumber: "m-002",
		})
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
	})

	t.Run("leaves already-correct hashes untouched", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "already", Surname: "lower", MemberNumber: "m-003", CreatedAt: time.Now(),
		}
		seedLegacyParticipant(t, censusID, member,
			HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields))

		report := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(report.ParticipantsScanned, qt.Equals, 1)
		c.Assert(report.ParticipantsRehashed, qt.Equals, 0)
		c.Assert(report.CensusesAffected, qt.Equals, 0)
	})

	t.Run("recomputes the email and phone variants of a two-factor census", func(_ *testing.T) {
		reset()
		twoFa := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone}
		census, censusID := newCensus(
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname}, twoFa)

		phone, err := NewHashedPhone(testPlaintextPhone, org)
		c.Assert(err, qt.IsNil)
		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Mixed", Surname: "Case", Email: "mixed.case@example.com",
			Phone: phone, CreatedAt: time.Now(),
		}
		ctx := context.Background()
		_, err = testDB.orgMembers.InsertOne(ctx, member)
		c.Assert(err, qt.IsNil)
		_, err = testDB.censusParticipants.InsertOne(ctx, &CensusParticipant{
			ParticipantID: member.ID.Hex(),
			CensusID:      censusID,
			LoginHash:     unfoldedHash(*member, census.AuthFields, census.TwoFaFields),
			LoginHashEmail: unfoldedHash(*member, census.AuthFields,
				OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}),
			LoginHashPhone: unfoldedHash(*member, census.AuthFields,
				OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone}),
			CreatedAt: time.Now(),
		})
		c.Assert(err, qt.IsNil)

		runRepair(t, migrations.RepairOptions{Apply: true})

		got := storedParticipant(member.ID.Hex(), censusID)
		c.Assert(got.LoginHash, qt.DeepEquals,
			HashAuthTwoFaFields(*member, census.AuthFields, census.TwoFaFields))
		c.Assert(got.LoginHashEmail, qt.DeepEquals,
			HashAuthTwoFaFields(*member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}))
		c.Assert(got.LoginHashPhone, qt.DeepEquals,
			HashAuthTwoFaFields(*member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone}))

		// The phone is hashed bytes and must never be folded, so a phone-based
		// login still resolves after the rewrite.
		found, err := testDB.CensusParticipantByLoginHash(*census, OrgMember{
			OrgAddress: testOrgAddress, Name: "mixed", Surname: "case", Phone: phone,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, member.ID.Hex())
	})

	t.Run("dry run reports without writing", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Dry ", Surname: "Run", MemberNumber: "M-004", CreatedAt: time.Now(),
		}
		legacy := unfoldedHash(*member, census.AuthFields, census.TwoFaFields)
		seedLegacyParticipant(t, censusID, member, legacy)

		report := runRepair(t, migrations.RepairOptions{Apply: false})
		c.Assert(report.MembersTrimmed, qt.Equals, 1)
		c.Assert(report.ParticipantsRehashed, qt.Equals, 1)

		c.Assert(storedMember(member.ID).Name, qt.Equals, "Dry ")
		c.Assert(storedParticipant(member.ID.Hex(), censusID).LoginHash, qt.DeepEquals, legacy)
	})

	t.Run("skips participants whose recomputed hash would collide", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		// Two members differing only by case: folding maps them onto one hash.
		upper := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Casey", Surname: "Clash", MemberNumber: "M-005", CreatedAt: time.Now(),
		}
		lower := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "casey", Surname: "clash", MemberNumber: "m-005", CreatedAt: time.Now(),
		}
		upperHash := unfoldedHash(*upper, census.AuthFields, census.TwoFaFields)
		lowerHash := unfoldedHash(*lower, census.AuthFields, census.TwoFaFields)
		seedLegacyParticipant(t, censusID, upper, upperHash)
		seedLegacyParticipant(t, censusID, lower, lowerHash)

		report := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(report.ParticipantsSkipped, qt.Equals, 2)
		c.Assert(report.ParticipantsRehashed, qt.Equals, 0)
		c.Assert(report.SkippedMembers, qt.HasLen, 2)

		// Both rows keep exactly what they had.
		c.Assert(storedParticipant(upper.ID.Hex(), censusID).LoginHash, qt.DeepEquals, upperHash)
		c.Assert(storedParticipant(lower.ID.Hex(), censusID).LoginHash, qt.DeepEquals, lowerHash)
	})

	t.Run("leaves participants whose member is gone alone", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)

		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Ghost", Surname: "Member", MemberNumber: "M-006", CreatedAt: time.Now(),
		}
		legacy := unfoldedHash(*member, census.AuthFields, census.TwoFaFields)
		seedLegacyParticipant(t, censusID, member, legacy)
		_, err := testDB.orgMembers.DeleteOne(context.Background(), bson.M{"_id": member.ID})
		c.Assert(err, qt.IsNil)

		report := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(report.OrphanParticipants, qt.Equals, 1)
		c.Assert(report.ParticipantsRehashed, qt.Equals, 0)
		c.Assert(storedParticipant(member.ID.Hex(), censusID).LoginHash, qt.DeepEquals, legacy)
	})

	t.Run("is idempotent", func(_ *testing.T) {
		reset()
		census, censusID := newCensus(authFields, noTwoFa)
		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: " Ida", Surname: "Potent ", MemberNumber: "M-007", CreatedAt: time.Now(),
		}
		seedLegacyParticipant(t, censusID, member,
			unfoldedHash(*member, census.AuthFields, census.TwoFaFields))

		first := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(first.MembersTrimmed, qt.Equals, 1)
		c.Assert(first.ParticipantsRehashed, qt.Equals, 1)

		second := runRepair(t, migrations.RepairOptions{Apply: true})
		c.Assert(second.MembersTrimmed, qt.Equals, 0)
		c.Assert(second.ParticipantsRehashed, qt.Equals, 0)
	})

	t.Run("restricts the run to the selected organization", func(_ *testing.T) {
		reset()
		other := &Organization{Address: testAnotherOrgAddress, CreatedAt: time.Now()}
		c.Assert(testDB.SetOrganization(other), qt.IsNil)

		mine, mineID := newCensus(authFields, noTwoFa)
		mineMember := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: "Mine", Surname: "Org", MemberNumber: "M-008", CreatedAt: time.Now(),
		}
		seedLegacyParticipant(t, mineID, mineMember,
			unfoldedHash(*mineMember, mine.AuthFields, mine.TwoFaFields))

		theirs := &Census{
			OrgAddress: testAnotherOrgAddress, AuthFields: authFields,
			TwoFaFields: noTwoFa, CreatedAt: time.Now(),
		}
		theirsID, err := testDB.SetCensus(theirs)
		c.Assert(err, qt.IsNil)
		theirsMember := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testAnotherOrgAddress,
			Name: "Their", Surname: "Org", MemberNumber: "M-009", CreatedAt: time.Now(),
		}
		theirsHash := unfoldedHash(*theirsMember, theirs.AuthFields, theirs.TwoFaFields)
		seedLegacyParticipant(t, theirsID, theirsMember, theirsHash)

		report := runRepair(t, migrations.RepairOptions{Apply: true, OrgAddress: &testOrgAddress})
		c.Assert(report.ParticipantsRehashed, qt.Equals, 1)

		// The other organization's participant is untouched.
		c.Assert(storedParticipant(theirsMember.ID.Hex(), theirsID).LoginHash, qt.DeepEquals, theirsHash)
	})
}

// TestRepairMatchesCanonicalHash guards the duplicated hash logic.
//
// migrations.hashMemberFields is a byte-for-byte mirror of HashAuthTwoFaFields,
// kept so the repair can work straight from bson documents without importing db.
// If the two ever disagree, the repair writes hashes the login path can never
// match and locks out every voter it "repaired". This drives a matrix of field
// combinations through the real repair and asserts each hash it wrote equals the
// canonical one, then resolves it through the real login lookup.
func TestRepairMatchesCanonicalHash(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	org := &Organization{Address: testOrgAddress, CreatedAt: time.Now(), Country: "ES"}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	phone, err := NewHashedPhone(testPlaintextPhone, org)
	c.Assert(err, qt.IsNil)

	combos := []struct {
		auth  OrgMemberAuthFields
		twoFa OrgMemberTwoFaFields
	}{
		{OrgMemberAuthFields{OrgMemberAuthFieldsName}, OrgMemberTwoFaFields{}},
		{OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname}, OrgMemberTwoFaFields{}},
		{OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}},
		{OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}},
		{OrgMemberAuthFields{OrgMemberAuthFieldsBirthDate}, OrgMemberTwoFaFields{}},
		{
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname},
			OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
		},
	}

	type seeded struct {
		memberID bson.ObjectID
		censusID string
		census   Census
	}
	var all []seeded
	for i, combo := range combos {
		census := &Census{
			OrgAddress: testOrgAddress, AuthFields: combo.auth,
			TwoFaFields: combo.twoFa, CreatedAt: time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		census.ID, err = bson.ObjectIDFromHex(censusID)
		c.Assert(err, qt.IsNil)

		// Deliberately mixed case and padded, so every rule has to fire.
		member := OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress,
			Name: " MiXeD ", Surname: "CaSe ", MemberNumber: "M-00" + string(rune('A'+i)),
			NationalID: "DnI00" + string(rune('1'+i)), BirthDate: "1990-01-02",
			Email: "person@example.com", Phone: phone, CreatedAt: time.Now(),
		}
		ctx := context.Background()
		_, err = testDB.orgMembers.InsertOne(ctx, member)
		c.Assert(err, qt.IsNil)
		_, err = testDB.censusParticipants.InsertOne(ctx, &CensusParticipant{
			ParticipantID: member.ID.Hex(), CensusID: censusID,
			LoginHash: unfoldedHash(member, combo.auth, combo.twoFa), CreatedAt: time.Now(),
		})
		c.Assert(err, qt.IsNil)
		all = append(all, seeded{memberID: member.ID, censusID: censusID, census: *census})
	}

	runRepair(t, migrations.RepairOptions{Apply: true})

	for _, s := range all {
		// Phase A trims the member, so the canonical hash is computed from the
		// trimmed values as they now sit in the database.
		var stored OrgMember
		c.Assert(testDB.orgMembers.FindOne(context.Background(),
			bson.M{"_id": s.memberID}).Decode(&stored), qt.IsNil)

		var got CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(context.Background(),
			bson.M{"participantID": s.memberID.Hex(), "censusId": s.censusID}).Decode(&got), qt.IsNil)

		c.Assert(got.LoginHash, qt.DeepEquals,
			HashAuthTwoFaFields(stored, s.census.AuthFields, s.census.TwoFaFields),
			qt.Commentf("repair and db disagree for auth=%v twoFa=%v",
				s.census.AuthFields, s.census.TwoFaFields))

		found, err := testDB.CensusParticipantByLoginHash(s.census, stored)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, s.memberID.Hex())
	}
}
