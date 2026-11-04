package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestNationalIDWarnings pins the warning collector both census validation and import use: one
// entry per value that cannot sign in (letter_mismatch, invalid, unsupported_document, missing),
// and a duplicate entry for every holder of a shared canonical value. canonical and derived_letter
// values are registered for duplicate grouping but emit no individual warning.
func TestNationalIDWarnings(t *testing.T) {
	c := qt.New(t)

	empty := newNationalIDWarnings().result()
	c.Assert(empty, qt.IsNotNil)
	c.Assert(empty, qt.HasLen, 0)

	n := newNationalIDWarnings()
	for _, m := range []struct{ id, value string }{
		{"a", "12345678Z"},  // canonical — no individual warning
		{"b", "12345678"},   // derived_letter (letterless) — no individual warning
		{"c", "12345678A"},  // letter_mismatch
		{"d", "K1234567L"},  // unsupported_document
		{"e", "PAB123456"},  // invalid
		{"f", ""},           // missing
		{"g", "02345678T"},  // canonical
		{"h", "2345678T"},   // canonical (padded form of g)
		{"i", "X1234567L"},  // canonical — singleton, no duplicate
	} {
		n.add(NationalIDWarning{MemberID: m.id}, m.value)
	}
	c.Assert(n.result(), qt.DeepEquals, []NationalIDWarning{
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

// TestNationalIDImportWarnings pins the import flavour: line-numbered, empty values skipped, and
// duplicates checked within the submitted list only.
func TestNationalIDImportWarnings(t *testing.T) {
	c := qt.New(t)
	members := []*OrgMember{
		{NationalID: "12345678"},
		{NationalID: "12.345.678-z"},
		{NationalID: ""},
		{NationalID: "12345678A"},
		{NationalID: "87654321X"},
	}
	c.Assert(nationalIDImportWarnings(members), qt.DeepEquals, []NationalIDWarning{
		{Line: 4, Class: "letter_mismatch"},
		{Line: 1, Class: NationalIDWarningDuplicate},
		{Line: 2, Class: NationalIDWarningDuplicate},
	})
	c.Assert(nationalIDImportWarnings([]*OrgMember{{NationalID: "87654321X"}, {}}), qt.HasLen, 0)
}
