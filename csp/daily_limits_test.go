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

	// ... and so are new tokens, even after the cooldown
	time.Sleep(2 * cooldown)
	_, err = newToken(testAnchorID)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)

	// the budget is per member and process: another process is unaffected
	_, err = newToken(otherAnchorID)
	c.Assert(err, qt.IsNil)
}
