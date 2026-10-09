package csp

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
)

func TestDailyChallengeSendCap(t *testing.T) {
	c := qt.New(t)
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	cooldown := 10 * time.Millisecond
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: cooldown,
		MaxDailyChallengeSends:   2,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	// a dedicated inbox: these challenges are delivered asynchronously, so sending
	// them to the shared address would pollute the other tests' OTP fetches
	dailyCapEmail := "daily-cap@test.com"
	otherAnchorID := internal.HexBytes("otherAnchorID")
	newToken := func(anchorID internal.HexBytes) (internal.HexBytes, error) {
		return csp.AuthToken(anchorID, testUserID, dailyCapEmail,
			notifications.EmailChallenge, apicommon.DefaultLang, "", "", testAddress.Address())
	}

	// first send of the day: a new token
	token, err := newToken(testAnchorID)
	c.Assert(err, qt.IsNil)

	// second send: a resend of the same token, counting against the same daily budget
	c.Assert(csp.ResendChallenge(token, dailyCapEmail, notifications.EmailChallenge,
		apicommon.DefaultLang, "", "", testAddress.Address()), qt.IsNil)

	// the daily budget (2) is spent: further resends are refused even though the
	// per-token resend cap (3) is not reached
	err = csp.ResendChallenge(token, dailyCapEmail, notifications.EmailChallenge,
		apicommon.DefaultLang, "", "", testAddress.Address())
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	// the refusal tells when the daily window resets
	assertRetryAfter(c, err, 24*time.Hour)

	// ... and so are new tokens, even after the cooldown
	time.Sleep(2 * cooldown)
	_, err = newToken(testAnchorID)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)

	// the budget is per member and process: another process is unaffected
	_, err = newToken(otherAnchorID)
	c.Assert(err, qt.IsNil)
}

func TestDailyChallengeSendReleasedWhenNothingIsSent(t *testing.T) {
	c := qt.New(t)
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	cooldown := 10 * time.Millisecond
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: cooldown,
		MaxDailyChallengeSends:   1,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	email := "daily-release@test.com"
	newToken := func(ctype notifications.ChallengeType) error {
		_, err := csp.AuthToken(testAnchorID, testUserID, email, ctype, apicommon.DefaultLang, "", "", testAddress.Address())
		return err
	}
	// a challenge that cannot be composed (unknown type) is never queued...
	c.Assert(newToken("bogus"), qt.ErrorIs, ErrNotificationFailure)
	// ... so it does not spend the only send of the day
	time.Sleep(2 * cooldown)
	c.Assert(newToken(notifications.EmailChallenge), qt.IsNil)
	// which a queued one does
	time.Sleep(2 * cooldown)
	c.Assert(newToken(notifications.EmailChallenge), qt.ErrorIs, errors.ErrVerificationMaxAttempts)
}

func TestAuthAttemptBudget(t *testing.T) {
	c := qt.New(t)
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)

	const budget = 2
	csp, err := New(context.Background(), &Config{
		DB:                    testDB,
		MailService:           testMailService,
		SMSService:            testSMSService,
		MaxHourlyAuthFailures: budget,
		RootKey:               *testRootKey,
	})
	c.Assert(err, qt.IsNil)

	identity := []byte("claimed-identity")
	otherIdentity := []byte("other-identity")
	otherAnchorID := internal.HexBytes("otherAnchorID")

	// released attempts (the ones that did not fail on the submitted data) never run out
	for range 3 * budget {
		cl, err := csp.ClaimAuthAttempt(testAnchorID, identity)
		c.Assert(err, qt.IsNil)
		csp.ReleaseAuthAttempt(cl)
	}

	// kept attempts (failures) exhaust the identity's budget...
	for range budget {
		_, err := csp.ClaimAuthAttempt(testAnchorID, identity)
		c.Assert(err, qt.IsNil)
	}
	_, err = csp.ClaimAuthAttempt(testAnchorID, identity)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	// ... for the rest of the current hour only
	assertRetryAfter(c, err, time.Hour)

	// ... and only that identity's, on that process: there is no process-wide budget that junk
	// attempts could spend to lock every voter out
	_, err = csp.ClaimAuthAttempt(testAnchorID, otherIdentity)
	c.Assert(err, qt.IsNil)
	_, err = csp.ClaimAuthAttempt(otherAnchorID, identity)
	c.Assert(err, qt.IsNil)
}

// assertRetryAfter checks that err is an API error carrying retryAfter, a whole number of
// seconds between 1 and the counter's window.
func assertRetryAfter(c *qt.C, err error, window time.Duration) {
	c.Helper()
	var apiErr errors.Error
	c.Assert(errors.As(err, &apiErr), qt.IsTrue)
	data, ok := apiErr.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", apiErr.Data))
	retryAfter, ok := data["retryAfter"].(int64)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", apiErr.Data))
	c.Assert(retryAfter >= 1, qt.IsTrue)
	c.Assert(retryAfter <= int64(window.Seconds()), qt.IsTrue)
}
