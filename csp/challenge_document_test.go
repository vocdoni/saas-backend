package csp

import (
	"context"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	saasNotifications "github.com/vocdoni/saas-backend/notifications" //revive:disable:import-alias-naming
	"github.com/vocdoni/saas-backend/test"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// newChallengeCSP returns a CSP on a fresh database with synchronous delivery, the given cooldown
// and mail service (nil: none), an hour-long code TTL and the given daily cap (0: the default).
func newChallengeCSP(c *qt.C, coolDown time.Duration, mail saasNotifications.NotificationService, daily int) *CSP {
	c.Helper()
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              mail,
		SMSService:               testSMSService,
		NotificationCoolDownTime: coolDown,
		NotificationTTL:          time.Hour,
		MaxDailyChallengeSends:   daily,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)
	return csp
}

// stepZero runs AuthToken for the test user and anchor.
func stepZero(csp *CSP, to string, ctype notifications.ChallengeType) (internal.HexBytes, error) {
	return csp.AuthToken(testAnchorID, testUserID, to, ctype, apicommon.DefaultLang,
		testOrgName, testOrgLogo, testAddress.Address())
}

// eventually polls cond for up to 5 s: a delivery result is settled by the queue consumer, which
// runs after even a synchronous push has returned.
func eventually(c *qt.C, cond func() bool) {
	c.Helper()
	for range 50 {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.Fatalf("condition not met within 5s")
}

// dailySendsLeft reports whether the test user can still be sent a challenge today on the test
// anchor under a cap of max.
func dailySendsLeft(c *qt.C, csp *CSP, maxSends int) bool {
	reached, err := csp.Storage.CSPCounterReached(db.CSPCounterChallengeSends,
		counterKey(testUserID, testAnchorID), maxSends)
	c.Assert(err, qt.IsNil)
	return !reached
}

func TestDestinationKey(t *testing.T) {
	c := qt.New(t)
	key := DestinationKey(notifications.EmailChallenge, "Voter@Example.com")
	c.Assert(key, qt.Equals, DestinationKey(notifications.EmailChallenge, "voter@example.com"))
	c.Assert(key, qt.HasLen, 32)
	c.Assert(key, qt.Not(qt.Equals), DestinationKey(notifications.SMSChallenge, "voter@example.com"))
	c.Assert(DestinationKey(notifications.SMSChallenge, "+34600000001"), qt.Not(qt.Equals),
		DestinationKey(notifications.SMSChallenge, "+34600000002"))
}

// TestChallengeSendRollback checks that a send that never reached the voter gives back its claim on
// the destination, its resend and its daily-cap unit (AC2).
func TestChallengeSendRollback(t *testing.T) {
	c := qt.New(t)

	c.Run("async delivery failure", func(c *qt.C) {
		// no mail service: the queue gives every email up and reports Success=false
		csp := newChallengeCSP(c, time.Hour, nil, 1)
		const email = "rollback-async@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		settled := func() bool {
			data, err := csp.Storage.CSPAuth(first)
			c.Assert(err, qt.IsNil)
			return len(data.Sends) == 0 && data.Resends == 0 && dailySendsLeft(c, csp, 1)
		}
		eventually(c, settled)

		// the destination is due at once: a repeated step 0 re-sends, which fails and is rolled
		// back the same way, resend included
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(second, qt.Not(qt.DeepEquals), first)
		eventually(c, settled)
	})

	c.Run("resend whose message cannot be built", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 2)
		const email = "rollback-compose@example.com"
		token, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		_ = fetchOTPCodeFromEmail(c, email)
		// the claim goes through for the submitted destination, then composing fails
		c.Assert(resendTo(csp, token, email, "bogus"), qt.ErrorIs, ErrNotificationFailure)
		data, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 0)
		c.Assert(data.Sends, qt.HasLen, 1)
		_, ok := data.Sends[DestinationKey("bogus", email)]
		c.Assert(ok, qt.IsFalse)
		// one of the two daily sends is left
		c.Assert(dailySendsLeft(c, csp, 2), qt.IsTrue)
	})
}

// TestChallengeReuseFailedSendDropsToken checks that a step 0 whose re-send of the pending code
// fails returns the error and leaves no extra token (AC3).
func TestChallengeReuseFailedSendDropsToken(t *testing.T) {
	c := qt.New(t)
	csp := newChallengeCSP(c, time.Hour, testMailService, 0)
	const email = "reuse-fail@example.com"
	first, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)

	_, err = stepZero(csp, email, "bogus")
	c.Assert(err, qt.ErrorIs, ErrNotificationFailure)
	data, err := csp.Storage.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Tokens, qt.HasLen, 1)
	c.Assert(data.Resends, qt.Equals, 0)
}

// TestChallengeResendRace checks that of concurrent resends to one destination exactly one sends,
// and the others report the wait left from the send that won (AC4).
func TestChallengeResendRace(t *testing.T) {
	c := qt.New(t)
	csp := newChallengeCSP(c, time.Hour, testMailService, 0)
	const emailA, emailB = "race-a@example.com", "race-b@example.com"
	token, err := stepZero(csp, emailA, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailA)

	const racers = 3
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() { errs[i] = resendTo(csp, token, emailB, notifications.EmailChallenge) })
	}
	wg.Wait()
	sent := 0
	for _, err := range errs {
		if err == nil {
			sent++
			continue
		}
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		var apiErr errors.Error
		c.Assert(errors.As(err, &apiErr), qt.IsTrue)
		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		coolDown, ok := data["coolDownTime"].(int64)
		c.Assert(ok, qt.IsTrue)
		c.Assert(coolDown > (59*time.Minute).Milliseconds(), qt.IsTrue)
	}
	c.Assert(sent, qt.Equals, 1)
	_ = fetchOTPCodeFromEmail(c, emailB)
	data, err := csp.Storage.CSPAuth(token)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Resends, qt.Equals, 1)
}

// TestChallengeSendPerDestination checks that a step 0 naming another destination of the voter
// inside the cooldown sends the code there, at most once per destination per cooldown and at most
// MaxChallengeResends times per code (AC5), and that the casing of an email is not another
// destination (AC12).
func TestChallengeSendPerDestination(t *testing.T) {
	c := qt.New(t)
	const phone = "+34600000077"
	smsDelivered := func() bool { return testSMSService.ConsumeSMS(phone) != nil }
	emailDelivered := func(email string) bool {
		_, err := testMailService.FindEmail(context.Background(), email)
		return err == nil
	}

	c.Run("once per destination per cooldown", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 0)
		const email = "per-dest@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered(email), qt.IsTrue)
		_, err = stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(smsDelivered(), qt.IsTrue)
		for range 2 {
			_, err = stepZero(csp, email, notifications.EmailChallenge)
			c.Assert(err, qt.IsNil)
			c.Assert(emailDelivered(email), qt.IsFalse)
			_, err = stepZero(csp, phone, notifications.SMSChallenge)
			c.Assert(err, qt.IsNil)
			c.Assert(smsDelivered(), qt.IsFalse)
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 1)
		c.Assert(data.Sends, qt.HasLen, 2)
	})

	c.Run("alternating destinations spend the code's resends", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Millisecond, testMailService, 0)
		const email = "alternate@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered(email), qt.IsTrue)
		// SMS, email, SMS: the code's three resends; then nothing more is sent, the token still is
		for i, want := range []bool{true, true, true, false, false} {
			time.Sleep(10 * time.Millisecond)
			if i%2 == 0 {
				_, err = stepZero(csp, phone, notifications.SMSChallenge)
				c.Assert(err, qt.IsNil)
				c.Assert(smsDelivered(), qt.Equals, want)
			} else {
				_, err = stepZero(csp, email, notifications.EmailChallenge)
				c.Assert(err, qt.IsNil)
				c.Assert(emailDelivered(email), qt.Equals, want)
			}
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, MaxChallengeResends)
	})

	c.Run("email casing", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 0)
		first, err := stepZero(csp, "casing@example.com", notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered("casing@example.com"), qt.IsTrue)
		for _, spelling := range []string{"Casing@Example.com", "CASING@EXAMPLE.COM"} {
			_, err = stepZero(csp, spelling, notifications.EmailChallenge)
			c.Assert(err, qt.IsNil)
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 0)
		c.Assert(data.Sends, qt.HasLen, 1)
	})
}

// TestMigratedLTSChallenges checks that rows stored as lts stores them, once migrated, verify,
// re-send and sign (AC7).
func TestMigratedLTSChallenges(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	dbName := test.RandomDatabaseName()
	testDB, err := db.New(testMongoURI, dbName)
	c.Assert(err, qt.IsNil)
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Hour,
		NotificationTTL:          time.Hour,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)

	database := testDB.DBClient.Database(dbName)
	now := time.Now().Truncate(time.Millisecond)
	secret := gotp.RandomSecret(16)
	pending, verified, authOnly := newAuthToken(), newAuthToken(), newAuthToken()
	pendingUser := internal.HexBytes([]byte("pendingUser"))
	verifiedUser := internal.HexBytes([]byte("verifiedUser"))
	authOnlyUser := internal.HexBytes([]byte("authOnlyUser"))
	_, err = database.Collection("cspTokens").InsertMany(ctx, []any{
		bson.M{
			"_id": pending, "userid": pendingUser, "bundleid": testAnchorID, "createdat": now,
			"secret": secret, "attempts": 0, "verified": false, "verifiedat": time.Time{}, "resends": 0,
		},
		bson.M{
			"_id": verified, "userid": verifiedUser, "bundleid": testAnchorID, "createdat": now,
			"attempts": 1, "verified": true, "verifiedat": now, "resends": 0,
		},
		bson.M{
			"_id": authOnly, "userid": authOnlyUser, "bundleid": testAnchorID, "createdat": now,
			"attempts": 0, "verified": true, "verifiedat": now,
		},
	})
	c.Assert(err, qt.IsNil)
	mig, ok := migrations.AsMap()[40]
	c.Assert(ok, qt.IsTrue)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	sign := func(token internal.HexBytes) {
		_, err := csp.Sign(token, testAddress, internal.HexBytes(util.RandomBytes(32)), testUserWeightBytes,
			signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
	}
	// the pending code re-sends, verifies and signs
	const email = "migrated-pending@example.com"
	c.Assert(resendTo(csp, pending, email, notifications.EmailChallenge), qt.IsNil)
	code := fetchOTPCodeFromEmail(c, email)
	c.Assert(code, qt.Equals, gotp.NewDefaultHOTP(secret).At(0))
	c.Assert(csp.VerifyAuthToken(pending, code), qt.IsNil)
	sign(pending)
	// verified and auth-only rows sign as they are
	sign(verified)
	sign(authOnly)
}
