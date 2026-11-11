package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestPrepareOrgMemberNationalID pins that a member's nationalId is stored as entered, only trimmed:
// DNI/NIE variants are canonicalised in the login-hash layer (calculateParticipantHashes), never at
// rest, and no derivation marker is written.
func TestPrepareOrgMemberNationalID(t *testing.T) {
	c := qt.New(t)
	org := &Organization{Address: common.Address{0x42}, Country: "ES"}
	prepare := func(m *OrgMember) *OrgMember {
		member, errs, err := prepareOrgMember(org, m, "salt", time.Now(), backgroundHasher)
		c.Assert(err, qt.IsNil)
		c.Assert(errs, qt.HasLen, 0)
		return member
	}

	inputs := []struct{ in, stored string }{
		{"12.345.678-z", "12.345.678-z"},
		{" 12345678 ", "12345678"},
		{"12345678A", "12345678A"},
		{" 2345678 ", "2345678"},
		{"y1234567", "y1234567"},
		{"2345678t", "2345678t"},
		{" PAB123456 ", "PAB123456"},
		{"", ""},
	}
	for _, tc := range inputs {
		got := prepare(&OrgMember{NationalID: tc.in})
		c.Assert(got.NationalID, qt.Equals, tc.stored, qt.Commentf("input %q", tc.in))
	}

	c.Run("stored documents keep the value and carry no marker", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
		for _, tc := range inputs {
			if tc.in == "" {
				continue // an empty nationalId is not written at all
			}
			id, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: tc.in})
			c.Assert(err, qt.IsNil)
			doc := rawMemberDoc(c, id)
			c.Assert(doc["nationalId"], qt.Equals, tc.stored, qt.Commentf("input %q", tc.in))
			assertNoNationalIDMarker(c, doc)
		}
	})

	c.Run("updates store the value as sent, and echoing it back changes nothing", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		org := &Organization{Address: testOrgAddress, CreatedAt: time.Now()}
		c.Assert(testDB.SetOrganization(org), qt.IsNil)
		id, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "12345678", Name: "Ana"})
		c.Assert(err, qt.IsNil)
		memberID, err := bson.ObjectIDFromHex(id)
		c.Assert(err, qt.IsNil)
		upsert := func(update *OrgMemberUpdate) bson.M {
			update.ID = memberID
			_, created, err := testDB.UpsertOrgMemberAndCensusParticipants(context.Background(), org, update, "salt")
			c.Assert(err, qt.IsNil)
			c.Assert(created, qt.IsFalse)
			return rawMemberDoc(c, id)
		}

		// an update not carrying the nationalId, then one echoing the stored value as the app's
		// editor does: the stored document is unchanged but for updatedAt
		before := upsert(&OrgMemberUpdate{Name: new("Ana Maria")})
		after := upsert(&OrgMemberUpdate{NationalID: new("12345678")})
		delete(before, "updatedAt")
		delete(after, "updatedAt")
		c.Assert(after, qt.DeepEquals, before)
		c.Assert(after["nationalId"], qt.Equals, "12345678")

		// a new value, letterless or with separators, is stored exactly trimmed
		for _, tc := range []struct{ in, stored string }{
			{" 87654321 ", "87654321"},
			{"87.654.321-x", "87.654.321-x"},
		} {
			doc := upsert(&OrgMemberUpdate{NationalID: new(tc.in)})
			c.Assert(doc["nationalId"], qt.Equals, tc.stored)
			assertNoNationalIDMarker(c, doc)
		}
	})
}

// rawMemberDoc returns the stored orgMembers document of the member as raw bson, so assertions see
// every key it holds, not only those OrgMember decodes.
func rawMemberDoc(c *qt.C, id string) bson.M {
	memberID, err := bson.ObjectIDFromHex(id)
	c.Assert(err, qt.IsNil)
	var doc bson.M
	c.Assert(testDB.orgMembers.FindOne(context.Background(), bson.M{"_id": memberID}).Decode(&doc), qt.IsNil)
	return doc
}

// assertNoNationalIDMarker asserts the stored member document holds no nationalIdOriginal key, the
// derivation marker of the superseded canonical-storage design.
func assertNoNationalIDMarker(c *qt.C, doc bson.M) {
	c.Helper()
	_, ok := doc["nationalIdOriginal"]
	c.Assert(ok, qt.IsFalse, qt.Commentf("nationalId %q", doc["nationalId"]))
}

// TestOrgMembersByFieldAnyOf pins the []string form of OrgMembersByField: a member matching any of
// the values is returned, and an empty list or value is refused.
func TestOrgMembersByFieldAnyOf(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	a, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "12345678Z"})
	c.Assert(err, qt.IsNil)
	b, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "PAB123456"})
	c.Assert(err, qt.IsNil)

	got, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID,
		[]string{"12.345.678-Z", "12345678Z", "PAB123456"})
	c.Assert(err, qt.IsNil)
	ids := make([]string, 0, len(got))
	for _, m := range got {
		ids = append(ids, m.ID.Hex())
	}
	c.Assert(ids, qt.ContentEquals, []string{a, b})

	for _, bad := range [][]string{{}, {"12345678Z", ""}} {
		_, err := testDB.OrgMembersByField(testOrgAddress, OrgMemberLookupFieldNationalID, bad)
		c.Assert(err, qt.ErrorIs, ErrInvalidData)
	}
}

// TestCensusParticipantNationalIDVariants pins end-to-end hash equality: a member stored in one DNI
// variant (as entered, never rewritten) is found by the login lookup from any other variant of the
// same document, because calculateParticipantHashes canonicalises both sides (umbrella plan
// Appendix B, last row). CensusParticipantByVerbatimLoginHash, in contrast, matches only the hash
// of the member exactly as given.
func TestCensusParticipantNationalIDVariants(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	authByNationalID := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	lookup := func(census Census, typed string) (*CensusParticipant, error) {
		input := (&OrgMember{OrgAddress: testOrgAddress, NationalID: typed}).Normalized()
		return testDB.CensusParticipantByLoginHash(census, *input)
	}

	// one census per row, so rows sharing a canonical value never meet in a census
	for _, tc := range []struct {
		stored string
		typed  []string
	}{
		{"2.345.678-t", []string{"02345678T", "2345678T", "2.345.678-t", " 02.345.678 t "}},
		{"02345678T", []string{"2.345.678-t", "2345678", "02345678t"}},
		{"2345678T", []string{" 02.345.678 t ", "02345678T"}},
		{"12345678", []string{"12345678Z", "12345678", "12.345.678-z"}},
		{"12345678Z", []string{"12345678", "12.345.678"}},
	} {
		memberID, err := testDB.SetOrgMember("salt", &OrgMember{
			OrgAddress: testOrgAddress, NationalID: tc.stored, Weight: 1,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(rawMemberDoc(c, memberID)["nationalId"], qt.Equals, tc.stored)
		census := &Census{OrgAddress: testOrgAddress, AuthFields: authByNationalID}
		_, _, err = testDB.PopulateMembersCensus(census, []string{memberID})
		c.Assert(err, qt.IsNil)

		for _, typed := range tc.typed {
			found, err := lookup(*census, typed)
			c.Assert(err, qt.IsNil, qt.Commentf("stored %q, typed %q", tc.stored, typed))
			c.Assert(found.ParticipantID, qt.Equals, memberID)
		}
		// the member document is still as entered after the census build
		c.Assert(rawMemberDoc(c, memberID)["nationalId"], qt.Equals, tc.stored)
	}

	c.Run("the verbatim lookup matches only the exact raw hash", func(c *qt.C) {
		census := &Census{OrgAddress: testOrgAddress, AuthFields: authByNationalID, CreatedAt: time.Now()}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		census.ID, err = bson.ObjectIDFromHex(censusID)
		c.Assert(err, qt.IsNil)
		verbatim := func(typed string) (*CensusParticipant, error) {
			input := (&OrgMember{OrgAddress: testOrgAddress, NationalID: typed}).Normalized()
			return testDB.CensusParticipantByVerbatimLoginHash(*census, *input)
		}

		// a participant still on its raw hash, as migration 0036 leaves a collision survivor
		survivor := OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: "12345678"}
		_, err = testDB.censusParticipants.InsertOne(context.Background(), &CensusParticipant{
			ParticipantID: survivor.ID.Hex(), CensusID: censusID,
			LoginHash: HashAuthTwoFaFields(survivor, census.AuthFields, census.TwoFaFields), CreatedAt: time.Now(),
		})
		c.Assert(err, qt.IsNil)
		// a participant written now, on its canonical hash, stored as "2.345.678-t"
		memberID, err := testDB.SetOrgMember("salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: "2.345.678-t"})
		c.Assert(err, qt.IsNil)
		added, memberErrs, err := testDB.AddCensusParticipantsByMemberIDs(censusID, []string{memberID})
		c.Assert(err, qt.IsNil)
		c.Assert(memberErrs, qt.HasLen, 0)
		c.Assert(added, qt.Equals, 1)

		for _, typed := range []string{"12345678", " 12345678 "} {
			found, err := verbatim(typed)
			c.Assert(err, qt.IsNil, qt.Commentf("typed %q", typed))
			c.Assert(found.ParticipantID, qt.Equals, survivor.ID.Hex())
		}
		// the raw hash folds case, as HashAuthTwoFaFields does: a canonical value typed in
		// lowercase is still the same raw hash
		found, err := verbatim("02345678t")
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, memberID)

		for _, typed := range []string{
			"12345678Z",   // the survivor's canonical form: not its raw hash
			"12.345.678",  // another variant of the survivor's value
			"2.345.678-t", // the stored variant of a canonical-hashed participant
			"2345678T",
		} {
			_, err := verbatim(typed)
			c.Assert(err, qt.ErrorIs, ErrNotFound, qt.Commentf("typed %q", typed))
		}
		// and the canonical lookup does not reach the raw-hashed survivor
		_, err = lookup(*census, "12345678")
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})
}
