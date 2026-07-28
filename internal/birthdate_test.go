package internal

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestParseBirthDate(t *testing.T) {
	c := qt.New(t)

	type testCase struct {
		in          string
		wantDate    string
		expectError bool
	}

	for name, tc := range map[string]testCase{
		"iso":                {in: "2001-02-03", wantDate: "2001-02-03"},
		"iso_slash":          {in: "2001/02/03", wantDate: "2001-02-03"},
		"iso_spaces":         {in: "2001 02 03", wantDate: "2001-02-03"},
		"dd/mm":              {in: "03/02/2001", wantDate: "2001-02-03"},
		"dd-mm":              {in: "03-02-2001", wantDate: "2001-02-03"},
		"dd space":           {in: "03 02 2001", wantDate: "2001-02-03"},
		"same_day_month":     {in: "05/05/2025", wantDate: "2025-05-05"},
		"reject_mm_dd":       {in: "12/31/2025", expectError: true},
		"invalid_date":       {in: "32/01/2025", expectError: true},
		"invalid_characters": {in: "invalid-birthdate", expectError: true},
	} {
		tc := tc
		c.Run(name, func(c *qt.C) {
			parsed, normalized, err := ParseBirthDate(tc.in)
			if tc.expectError {
				c.Assert(err, qt.IsNotNil)
				return
			}

			c.Assert(err, qt.IsNil)
			c.Assert(normalized, qt.Equals, tc.wantDate)

			wantTime, _ := time.Parse(time.DateOnly, tc.wantDate)
			c.Assert(parsed.Equal(wantTime), qt.IsTrue)
		})
	}
}

// TestNormalizeBirthDate covers the never-failing wrapper used on both sides of
// the CSP login-hash comparison: stored birthdates are always canonical, so
// accepting the other formats ParseBirthDate understands can only turn a failed
// match into a successful one, and unparseable input must pass through rather
// than be rejected.
func TestNormalizeBirthDate(t *testing.T) {
	c := qt.New(t)

	// every accepted spelling below denotes this same date
	const canonical = "1990-01-02"

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"canonical is unchanged", canonical, canonical},
		{"surrounding whitespace is trimmed", "  " + canonical + " ", canonical},
		{"day-first is canonicalized", "02/01/1990", canonical},
		{"day-first with dashes is canonicalized", "02-01-1990", canonical},
		{"year-first with slashes is canonicalized", "1990/01/02", canonical},
		{"single-digit parts are padded", "1990-1-2", canonical},
		{"unparseable input is passed through trimmed", " not-a-date ", "not-a-date"},
		{"empty stays empty", "   ", ""},
	} {
		t.Run(tc.name, func(*testing.T) {
			c.Assert(NormalizeBirthDate(tc.input), qt.Equals, tc.want)
		})
	}
}
