package handlers

import (
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

func TestSignOutcome(t *testing.T) {
	c := qt.New(t)
	// each known sentinel maps to its stable code — including wrapped, the form csp.Sign
	// actually returns for signer failures — and everything else to the retryable catch-all.
	for _, tc := range []struct {
		err  error
		code string
	}{
		{csp.ErrProcessAlreadyConsumed, signCodeAlreadyConsumed},
		{csp.ErrUserAlreadySigning, signCodeAlreadySigning},
		{csp.ErrInvalidAuthToken, signCodeAuthInvalid},
		{csp.ErrAuthTokenNotVerified, signCodeAuthInvalid},
		{fmt.Errorf("wrapping: %w", csp.ErrProcessAlreadyConsumed), signCodeAlreadyConsumed},
		{csp.ErrSign, signCodeFailed},
		{fmt.Errorf("some storage blip"), signCodeFailed},
	} {
		code, message := signOutcome(tc.err)
		c.Assert(code, qt.Equals, tc.code, qt.Commentf("error: %v", tc.err))
		c.Assert(message, qt.Not(qt.Equals), "")
		// the sanitized message must not echo the raw error text
		c.Assert(message, qt.Not(qt.Contains), "blip")
	}
}

func TestValidateVoterAddress(t *testing.T) {
	c := qt.New(t)
	// qt.IsNil rejects a typed-nil *errors.Error (it implements error), so compare the pointer.
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength))),
		qt.Equals, (*errors.Error)(nil))
	c.Assert(validateVoterAddress(nil), qt.Not(qt.IsNil))
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength-1))), qt.Not(qt.IsNil))
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength+1))), qt.Not(qt.IsNil))
}

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
