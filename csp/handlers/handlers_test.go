package handlers

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
)

func TestHandlePhoneContact(t *testing.T) {
	c := qt.New(t)

	test := func(org *db.Organization, phone, want string) {
		hashedPhone, err := db.NewHashedPhone(phone, org)
		c.Assert(err, qt.IsNil)

		phone, challengeType, err := handlePhoneContact(org, phone, hashedPhone)
		c.Assert(err, qt.IsNil)
		c.Assert(phone, qt.Equals, want)
		c.Assert(challengeType, qt.Equals, notifications.SMSChallenge)
	}

	org := &db.Organization{
		Address:   common.Address{0x01, 0x23, 0x45, 0x67, 0x89},
		Active:    true,
		CreatedAt: time.Now(),
	}
	org.Country = "ES"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+34612345601") })

	org.Country = "FR"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+33612345601") })

	org.Country = "US" // a valid phone with prefix, should keep it regardless of org country
	t.Run(org.Country, func(*testing.T) { test(org, "+34612345601", "+34612345601") })
}

// TestNormalizeBirthDateInput checks that a birthdate supplied at login is
// canonicalized the same way it is at member-creation time. Stored birthdates
// are always YYYY-MM-DD, so accepting the other formats the importer accepts can
// only turn a failed login-hash match into a successful one.
func TestNormalizeBirthDateInput(t *testing.T) {
	c := qt.New(t)

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"canonical is unchanged", "1990-01-02", "1990-01-02"},
		{"surrounding whitespace is trimmed", "  1990-01-02 ", "1990-01-02"},
		{"day-first is canonicalized", "02/01/1990", "1990-01-02"},
		{"day-first with dashes is canonicalized", "02-01-1990", "1990-01-02"},
		{"year-first with slashes is canonicalized", "1990/01/02", "1990-01-02"},
		{"single-digit parts are padded", "1990-1-2", "1990-01-02"},
		{"unparseable input is passed through trimmed", " not-a-date ", "not-a-date"},
		{"empty stays empty", "   ", ""},
	} {
		t.Run(tc.name, func(*testing.T) {
			c.Assert(normalizeBirthDateInput(tc.input), qt.Equals, tc.want)
		})
	}
}
