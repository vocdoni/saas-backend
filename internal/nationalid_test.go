package internal

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestNormalizeNationalID runs the shared DNI/NIE vector table (umbrella plan Appendix B, letters
// corrected per the SP1 spec AC1). The semantics come from cert-check's internal/identity/document.go
// @ be40f07 (Document.Number, ValidDNI, ValidNIE, ErrUnsupportedDocument); cert-check is not imported.
func TestNormalizeNationalID(t *testing.T) {
	c := qt.New(t)

	for _, tc := range []struct {
		name, in  string
		class     NationalIDClass
		typ       NationalIDType
		canonical string
	}{
		{"canonical dni", "12345678Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"lowercase letter", "12345678z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"dots and dash", "12.345.678-Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"spaces around and inside", " 12 345 678 z ", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"nbsp separator", "12345678 Z", NationalIDCanonical, NationalIDTypeDNI, "12345678Z"},
		{"letterless derives", "12345678", NationalIDDerivedLetter, NationalIDTypeDNI, "12345678Z"},
		{"typo neighbour derives its own letter", "12345679", NationalIDDerivedLetter, NationalIDTypeDNI, "12345679S"},
		{"seven digits padded", "2345678T", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"padded with separators lowercase", "2.345.678-t", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"already padded", "02345678T", NationalIDCanonical, NationalIDTypeDNI, "02345678T"},
		{"short letterless padded", "1", NationalIDDerivedLetter, NationalIDTypeDNI, "00000001R"},
		{"55555555 is K", "55555555K", NationalIDCanonical, NationalIDTypeDNI, "55555555K"},
		{"05555555 is C", "5555555C", NationalIDCanonical, NationalIDTypeDNI, "05555555C"},
		{"55555565 is P", "55555565P", NationalIDCanonical, NationalIDTypeDNI, "55555565P"},
		{"wrong letter", "12345678A", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"letter outside the alphabet", "12345678I", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"55555555B is a wrong letter", "55555555B", NationalIDLetterMismatch, NationalIDTypeDNI, ""},
		{"nie x canonical", "X1234567L", NationalIDCanonical, NationalIDTypeNIE, "X1234567L"},
		{"nie y derives", "Y1234567", NationalIDDerivedLetter, NationalIDTypeNIE, "Y1234567X"},
		{"nie z canonical", "Z7654321H", NationalIDCanonical, NationalIDTypeNIE, "Z7654321H"},
		{"nie lowercase with separators", "x-1234567-l", NationalIDCanonical, NationalIDTypeNIE, "X1234567L"},
		{"nie short padded", "X123P", NationalIDCanonical, NationalIDTypeNIE, "X0000123P"},
		{"nie wrong letter", "X1234567A", NationalIDLetterMismatch, NationalIDTypeNIE, ""},
		{"k nif", "K1234567L", NationalIDUnsupportedDocument, "", ""},
		{"m nif", "M1234567A", NationalIDUnsupportedDocument, "", ""},
		{"cif shape", "B12345678", NationalIDUnsupportedDocument, "", ""},
		{"nine digits", "123456789", NationalIDInvalid, "", ""},
		{"nie with eight digits", "X12345678", NationalIDInvalid, "", ""},
		{"bare nie prefix", "Z", NationalIDInvalid, "", ""},
		{"passport", "PAB123456", NationalIDInvalid, "", ""},
		{"legacy test id", "DNI001", NationalIDInvalid, "", ""},
		{"only separators", "-.-", NationalIDInvalid, "", ""},
		{"fullwidth letter", "12345678Ｚ", NationalIDInvalid, "", ""},
		{"fullwidth digits", "１２３４５６７８Z", NationalIDInvalid, "", ""},
		{"cyrillic x lookalike", "Х1234567L", NationalIDInvalid, "", ""},
		{"accented letter", "12345678É", NationalIDInvalid, "", ""},
		{"zero width space", "12345678\u200bZ", NationalIDInvalid, "", ""},
		{"empty", "", NationalIDMissing, "", ""},
		{"whitespace only", " 	 ", NationalIDMissing, "", ""},
	} {
		c.Run(tc.name, func(c *qt.C) {
			got := NormalizeNationalID(tc.in)
			c.Assert(got.Class, qt.Equals, tc.class)
			c.Assert(got.Type, qt.Equals, tc.typ)
			c.Assert(got.Canonical, qt.Equals, tc.canonical)
			if tc.canonical != "" {
				c.Assert(got.Normalized, qt.Equals, tc.canonical)
			} else {
				// never rewritten: what a census keyed on it stores and hashes today
				c.Assert(got.Normalized, qt.Equals, strings.TrimSpace(tc.in))
			}
		})
	}

	c.Run("23-letter sweep", func(c *qt.C) {
		for _, letter := range nationalIDControlLetters {
			got := NormalizeNationalID("12345678" + string(letter))
			if letter == 'Z' {
				c.Assert(got.Class, qt.Equals, NationalIDCanonical)
				continue
			}
			c.Assert(got.Class, qt.Equals, NationalIDLetterMismatch, qt.Commentf("letter %c", letter))
		}
	})

	c.Run("certificate value matches every stored variant", func(c *qt.C) {
		// cert-check hands over 02345678T; the member was imported as 2.345.678-t
		c.Assert(NormalizeNationalID("2.345.678-t").Normalized, qt.Equals, NormalizeNationalID("02345678T").Normalized)
		// two members 2345678T and 02345678T share one canonical value (duplicate_after_normalization)
		c.Assert(NormalizeNationalID("2345678T").Canonical, qt.Equals, NormalizeNationalID("02345678T").Canonical)
	})
}

// FuzzNormalizeNationalID checks the properties the storage and login paths rely on: no panic,
// Normalized is idempotent, every canonical form re-validates as canonical, and a value without
// a canonical form is passed through trimmed.
func FuzzNormalizeNationalID(f *testing.F) {
	for _, seed := range []string{
		"12345678Z", "12345678", "12.345.678-z", "2345678T", "X1234567L", "Y1234567", "Z7654321H",
		"12345678A", "K1234567L", "B12345678", "123456789", "PAB123456", "", " ", " ", "-",
		"12345678Ｚ", "Х1234567L", "X", "00000000", "99999999", "Z9999999",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got := NormalizeNationalID(raw)
		again := NormalizeNationalID(got.Normalized)
		if again.Normalized != got.Normalized {
			t.Fatalf("not idempotent: %q -> %q -> %q", raw, got.Normalized, again.Normalized)
		}
		if got.Canonical == "" {
			if got.Normalized != strings.TrimSpace(raw) {
				t.Fatalf("%q without a canonical form was rewritten to %q", raw, got.Normalized)
			}
			return
		}
		if len(got.Canonical) != 9 || got.Normalized != got.Canonical {
			t.Fatalf("%q: bad canonical %q (normalized %q)", raw, got.Canonical, got.Normalized)
		}
		if again.Class != NationalIDCanonical || again.Canonical != got.Canonical {
			t.Fatalf("%q: canonical %q re-validates as %s %q", raw, got.Canonical, again.Class, again.Canonical)
		}
	})
}
