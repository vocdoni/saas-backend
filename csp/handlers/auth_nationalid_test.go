package handlers

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// TestVerbatimNationalIDLogin pins when auth/0 tries the typed nationalId before the normalized
// one: only on a census logging in with the nationalId, and only when normalization rewrote it.
func TestVerbatimNationalIDLogin(t *testing.T) {
	c := qt.New(t)
	withID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	withoutID := &db.Census{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}}

	for _, tc := range []struct {
		typed    string
		verbatim string // "" when no verbatim lookup runs
	}{
		{"12345678", "12345678"},
		{" 12.345.678-Z ", "12.345.678-Z"},
		{"2345678T", "2345678T"},
		{"12345678Z", ""},
		{" 12345678z ", ""},
		{"12345678A", ""},
		{"12.345.678-A", ""},
		{"PAB123456", ""},
	} {
		input := (&db.OrgMember{NationalID: tc.typed, MemberNumber: "M-1"}).Normalized()
		got := verbatimNationalIDLogin(withID, input, tc.typed)
		if tc.verbatim == "" {
			c.Assert(got, qt.IsNil, qt.Commentf("typed %q", tc.typed))
		} else {
			c.Assert(got, qt.Not(qt.IsNil), qt.Commentf("typed %q", tc.typed))
			c.Assert(got.NationalID, qt.Equals, tc.verbatim)
			// only the nationalId differs from the normalized login
			c.Assert(got.MemberNumber, qt.Equals, input.MemberNumber)
		}
		c.Assert(verbatimNationalIDLogin(withoutID, input, tc.typed), qt.IsNil)
	}
}
