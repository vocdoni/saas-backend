package handlers

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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
		{csp.ErrAddressMismatch, signCodeAddressMismatch},
		{fmt.Errorf("wrapping: %w", csp.ErrProcessAlreadyConsumed), signCodeAlreadyConsumed},
		{csp.ErrSign, signCodeFailed},
		// the shape csp.Sign returns for a storage failure: joined, NOT an auth verdict, so a
		// transient Mongo error stays a retryable per-ballot outcome instead of aborting the batch
		{stderrors.Join(csp.ErrSign, fmt.Errorf("some storage blip")), signCodeFailed},
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
		CreatedAt: time.Now(),
	}
	org.Country = "ES"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+34612345601") })

	org.Country = "FR"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+33612345601") })

	org.Country = "US" // a valid phone with prefix, should keep it regardless of org country
	t.Run(org.Country, func(*testing.T) { test(org, "+34612345601", "+34612345601") })
}

// TestMemberPasswordMatches covers the password check behind auth step 0: it matches the stored
// argon2 hash of the client hash and nothing else, and a check waiting for a slot gives up, with its
// request or after a bounded wait, instead of blocking forever.
func TestMemberPasswordMatches(t *testing.T) {
	c := qt.New(t)
	h := New(&csp.CSP{PasswordSalt: "salt"}, nil)
	clientHash := strings.Repeat("ab", 32)
	stored := &db.OrgMember{ID: bson.NewObjectID()}
	stored.HashedPass = db.HashMemberPassword("salt", stored.ID, clientHash)

	ok, err := h.memberPasswordMatches(context.Background(), clientHash, stored)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	ok, err = h.memberPasswordMatches(context.Background(), strings.Repeat("cd", 32), stored)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// the hash is salted with the member ID: another member's stored hash does not match
	ok, err = h.memberPasswordMatches(context.Background(), clientHash,
		&db.OrgMember{ID: bson.NewObjectID(), HashedPass: stored.HashedPass})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// a member without a stored password never matches
	ok, err = h.memberPasswordMatches(context.Background(), clientHash, &db.OrgMember{ID: stored.ID})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// every slot taken: a cancelled request does not wait for one
	for range cap(h.passwordSlots) {
		h.passwordSlots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.memberPasswordMatches(ctx, clientHash, stored)
	c.Assert(err, qt.ErrorIs, context.Canceled)

	// nor does one waiting longer than passwordSlotWait: it tells the voter to retry later
	h.passwordSlotWait = time.Millisecond
	_, err = h.memberPasswordMatches(context.Background(), clientHash, stored)
	c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	c.Assert(passwordCheckFailed(err), qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	c.Assert(passwordCheckFailed(context.Canceled), qt.ErrorIs, errors.ErrGenericInternalServerError)
}
