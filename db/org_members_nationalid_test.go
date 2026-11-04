package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

// TestPrepareOrgMemberNationalID pins how a member's nationalId is stored: canonicalized, with the
// imported value kept in NationalIDOriginal only when the check letter was derived, and that marker
// surviving every write that does not carry a new nationalId.
func TestPrepareOrgMemberNationalID(t *testing.T) {
	c := qt.New(t)
	org := &Organization{Address: common.Address{0x42}, Country: "ES"}
	prepare := func(m *OrgMember) *OrgMember {
		member, errs, err := prepareOrgMember(org, m, "salt", time.Now(), backgroundHasher)
		c.Assert(err, qt.IsNil)
		c.Assert(errs, qt.HasLen, 0)
		return member
	}

	for _, tc := range []struct{ in, stored, original string }{
		{"12345678", "12345678Z", "12345678"},
		{" 2345678 ", "02345678T", "2345678"},
		{"y1234567", "Y1234567X", "y1234567"},
		{"12.345.678-z", "12345678Z", ""},
		{"2345678t", "02345678T", ""},
		{"12345678A", "12345678A", ""},
		{" PAB123456 ", "PAB123456", ""},
		{"", "", ""},
	} {
		got := prepare(&OrgMember{NationalID: tc.in})
		c.Assert(got.NationalID, qt.Equals, tc.stored, qt.Commentf("input %q", tc.in))
		c.Assert(got.NationalIDOriginal, qt.Equals, tc.original, qt.Commentf("input %q", tc.in))
	}

	stored := *prepare(&OrgMember{NationalID: "12345678", Name: "Ana"})
	c.Assert(stored.NationalIDOriginal, qt.Equals, "12345678")

	c.Run("an update not carrying nationalId keeps the marker", func(c *qt.C) {
		name := "Ana Maria"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, Name: &name}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "12345678Z")
		c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")
	})
	c.Run("an explicit lettered update to a different DNI clears the marker", func(c *qt.C) {
		lettered := "87654321X"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &lettered}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "87654321X")
		c.Assert(got.NationalIDOriginal, qt.Equals, "")
	})
	c.Run("an explicit letterless update records the new value", func(c *qt.C) {
		letterless := "87654321"
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &letterless}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "87654321X")
		c.Assert(got.NationalIDOriginal, qt.Equals, "87654321")
	})
	c.Run("clearing the nationalId clears the marker", func(c *qt.C) {
		empty := ""
		got := prepare((&OrgMemberUpdate{ID: stored.ID, NationalID: &empty}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "")
		c.Assert(got.NationalIDOriginal, qt.Equals, "")
	})
	c.Run("re-sending the canonical value when only email changes keeps the marker", func(c *qt.C) {
		email := "ana@example.com"
		canonical := stored.NationalID // "12345678Z", same value the app always echoes back
		got := prepare((&OrgMemberUpdate{ID: stored.ID, Email: &email, NationalID: &canonical}).applyTo(stored))
		c.Assert(got.NationalID, qt.Equals, "12345678Z")
		c.Assert(got.NationalIDOriginal, qt.Equals, "12345678")
	})
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

// TestCensusParticipantNationalIDVariants pins end-to-end hash equality: a member imported in one
// DNI variant is found by the login lookup from any other variant of the same document, because
// both sides go through OrgMember.Normalized (umbrella plan Appendix B, last row).
func TestCensusParticipantNationalIDVariants(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	// one census per row, so rows sharing a canonical value never meet in a census
	for _, tc := range []struct{ stored, typed string }{
		{"2.345.678-t", "02345678T"},
		{"02345678T", "2.345.678-t"},
		{"2345678T", " 02.345.678 t "},
		{"12345678", "12345678Z"},
		{"12345678Z", "12345678"},
	} {
		memberID, err := testDB.SetOrgMember("salt", &OrgMember{
			OrgAddress: testOrgAddress, NationalID: tc.stored, Weight: 1,
		})
		c.Assert(err, qt.IsNil)
		census := &Census{OrgAddress: testOrgAddress, AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}}
		_, _, err = testDB.PopulateMembersCensus(census, []string{memberID})
		c.Assert(err, qt.IsNil)

		input := (&OrgMember{OrgAddress: testOrgAddress, NationalID: tc.typed}).Normalized()
		found, err := testDB.CensusParticipantByLoginHash(*census, *input)
		c.Assert(err, qt.IsNil, qt.Commentf("stored %q, typed %q", tc.stored, tc.typed))
		c.Assert(found.ParticipantID, qt.Equals, memberID)
	}
}
