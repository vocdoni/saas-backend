package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestNationalIDWarnings pins the warning collector both census validation and import use: one
// entry per value that is not a supplied-letter DNI/NIE, derived letters carrying both values, and
// a duplicate entry for every holder of a shared canonical value.
func TestNationalIDWarnings(t *testing.T) {
	c := qt.New(t)

	empty := newNationalIDWarnings().result()
	c.Assert(empty, qt.IsNotNil)
	c.Assert(empty, qt.HasLen, 0)

	n := newNationalIDWarnings()
	for _, m := range []struct{ id, value, original string }{
		{"a", "12345678Z", "12345678"}, // stored canonical with its import-time marker
		{"b", "12345678", ""},          // stored letterless (a member migration 34 skipped)
		{"c", "12345678A", ""},
		{"d", "K1234567L", ""},
		{"e", "PAB123456", ""},
		{"f", "", ""},
		{"g", "02345678T", ""},
		{"h", "2345678T", ""},
		{"i", "X1234567L", ""},
	} {
		n.add(NationalIDWarning{MemberID: m.id}, m.value, m.original)
	}
	c.Assert(n.result(), qt.DeepEquals, []NationalIDWarning{
		{MemberID: "a", Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: "b", Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		{MemberID: "c", Class: "letter_mismatch"},
		{MemberID: "d", Class: "unsupported_document"},
		{MemberID: "e", Class: "invalid"},
		{MemberID: "f", Class: "missing"},
		{MemberID: "a", Class: NationalIDWarningDuplicate},
		{MemberID: "b", Class: NationalIDWarningDuplicate},
		{MemberID: "g", Class: NationalIDWarningDuplicate},
		{MemberID: "h", Class: NationalIDWarningDuplicate},
	})
}
