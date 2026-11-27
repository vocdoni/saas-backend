package csp

import (
	"context"
	"fmt"
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
// and mail service (nil: none), a code TTL one hour longer than the cooldown, so codes are
// reusable, and the given send ceiling (0: the default).
func newChallengeCSP(c *qt.C, coolDown time.Duration, mail saasNotifications.NotificationService, ceiling int) *CSP {
	c.Helper()
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              mail,
		SMSService:               testSMSService,
		NotificationCoolDownTime: coolDown,
		NotificationTTL:          coolDown + time.Hour,
		MaxDailyChallengeSends:   ceiling,
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

// backoffSends returns the sends the test user's send backoff on the test anchor holds.
func backoffSends(c *qt.C, csp *CSP) int {
	backoff, err := csp.Storage.CSPSendBackoff(counterKey(testUserID, testAnchorID))
	c.Assert(err, qt.IsNil)
	if backoff == nil {
		return 0
	}
	return len(backoff.Sends)
}

func TestDestinationKey(t *testing.T) {
	c := qt.New(t)
	key := DestinationKey(notifications.EmailChallenge, "Voter@Example.com")
	c.Assert(key, qt.Equals, DestinationKey(notifications.EmailChallenge, "voter@example.com"))
	// spellings strings.EqualFold matches share one key: long s, Kelvin sign
	c.Assert(DestinationKey(notifications.EmailChallenge, "\u017fuper@example.com"), qt.Equals,
		DestinationKey(notifications.EmailChallenge, "super@example.com"))
	c.Assert(DestinationKey(notifications.EmailChallenge, "\u212aate@example.com"), qt.Equals,
		DestinationKey(notifications.EmailChallenge, "kate@example.com"))
	c.Assert(key, qt.HasLen, 32)
	c.Assert(key, qt.Not(qt.Equals), DestinationKey(notifications.SMSChallenge, "voter@example.com"))
	c.Assert(DestinationKey(notifications.SMSChallenge, "+34600000001"), qt.Not(qt.Equals),
		DestinationKey(notifications.SMSChallenge, "+34600000002"))
}

// rejectingMailService is a mail provider that rejects every message permanently, so the queue
// gives up after a single provider call.
type rejectingMailService struct{}

func (rejectingMailService) New(any) error { return nil }

func (rejectingMailService) SendNotification(context.Context, *saasNotifications.Notification) error {
	return &saasNotifications.PermanentError{Err: fmt.Errorf("mailbox unavailable")}
}

// newRejectingCSP returns a CSP whose mail provider rejects every message and that has no SMS
// service, with one queue worker so results are settled in the order the sends were pushed.
func newRejectingCSP(c *qt.C, ceiling int) *CSP {
	c.Helper()
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              rejectingMailService{},
		NotificationCoolDownTime: time.Hour,
		NotificationTTL:          2 * time.Hour,
		NotificationQueueWorkers: 1,
		MaxDailyChallengeSends:   ceiling,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)
	return csp
}

// waitSettled waits until every send csp pushed so far has been settled. It pushes an SMS for
// another member, which no provider is called for, and waits for its rollback: with one worker
// the queue settles results in push order.
func waitSettled(c *qt.C, csp *CSP) {
	c.Helper()
	markerUser := internal.HexBytes(fmt.Sprintf("marker-%d", time.Now().UnixNano()))
	marker, err := csp.AuthToken(testAnchorID, markerUser, "+34600000099", notifications.SMSChallenge,
		apicommon.DefaultLang, testOrgName, testOrgLogo, testAddress.Address())
	c.Assert(err, qt.IsNil)
	eventually(c, func() bool {
		data, err := csp.Storage.CSPAuth(marker)
		c.Assert(err, qt.IsNil)
		return len(data.Sends) == 0
	})
}

// TestChallengeSendRollback checks that a send no provider was called for gives back its claim on
// the destination, its resend and its send in the backoff, while one a provider failed keeps them, as
// it may have been delivered (AC2).
func TestChallengeSendRollback(t *testing.T) {
	c := qt.New(t)

	c.Run("async give-up before any provider call", func(c *qt.C) {
		// no mail service: the queue gives every email up without a provider call
		csp := newChallengeCSP(c, time.Hour, nil, 1)
		const email = "rollback-async@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		settled := func() bool {
			data, err := csp.Storage.CSPAuth(first)
			c.Assert(err, qt.IsNil)
			return len(data.Sends) == 0 && data.Resends == 0 && backoffSends(c, csp) == 0
		}
		eventually(c, settled)

		// the destination is due at once: a repeated step 0 re-sends, which fails and is rolled
		// back the same way, resend included
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(second, qt.Not(qt.DeepEquals), first)
		eventually(c, settled)
	})

	c.Run("provider failure keeps the send", func(c *qt.C) {
		csp := newRejectingCSP(c, 1)
		const email = "provider-fail@example.com"
		token, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		waitSettled(c, csp)
		data, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		_, ok := data.Sends[DestinationKey(notifications.EmailChallenge, email)]
		c.Assert(ok, qt.IsTrue)
		c.Assert(backoffSends(c, csp), qt.Equals, 1)
	})

	c.Run("resend the provider fails keeps its resend", func(c *qt.C) {
		csp := newRejectingCSP(c, 2)
		const emailA, emailB = "provider-fail-a@example.com", "provider-fail-b@example.com"
		token, err := stepZero(csp, emailA, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(resendTo(csp, token, emailB, notifications.EmailChallenge), qt.IsNil)
		waitSettled(c, csp)
		data, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 1)
		c.Assert(data.Sends, qt.HasLen, 2)
		c.Assert(backoffSends(c, csp), qt.Equals, 2)
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
		// only the first send is held
		c.Assert(backoffSends(c, csp), qt.Equals, 1)
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

// TestChallengeUnsentDestination checks that a step 0 naming a destination the pending code was
// never sent to gets no token when the code cannot be sent there: it waits for a new code instead,
// while a destination that already got the code keeps getting tokens for it.
func TestChallengeUnsentDestination(t *testing.T) {
	c := qt.New(t)
	const phone = "+34600000088"
	smsDest := DestinationKey(notifications.SMSChallenge, phone)

	c.Run("resends spent", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 0)
		const email = "unsent-resends@example.com"
		emailDest := DestinationKey(notifications.EmailChallenge, email)
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		code := fetchOTPCodeFromEmail(c, email)
		csp.notificationCoolDownTime = time.Millisecond
		for range MaxChallengeResends {
			time.Sleep(10 * time.Millisecond)
			c.Assert(resendTo(csp, first, email, notifications.EmailChallenge), qt.IsNil)
			_ = fetchOTPCodeFromEmail(c, email)
		}
		csp.notificationCoolDownTime = time.Hour

		// the email holds the code: a token, nothing sent
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		assertNoEmailTo(c, email)
		c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, emailDest), qt.IsNil)
		// the phone never got it and cannot: the new code it needs waits out the cooldown
		c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, smsDest), qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		_, err = stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		c.Assert(testSMSService.ConsumeSMS(phone), qt.IsNil)
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Tokens, qt.HasLen, 2)

		// once the cooldown has passed, the phone gets a new code
		csp.notificationCoolDownTime = time.Millisecond
		time.Sleep(10 * time.Millisecond)
		third, err := stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(testSMSService.ConsumeSMS(phone), qt.Not(qt.IsNil))
		thirdData, err := csp.Storage.CSPAuth(third)
		c.Assert(err, qt.IsNil)
		c.Assert(thirdData.ID, qt.Not(qt.DeepEquals), data.ID)
		c.Assert(thirdData.Tokens, qt.HasLen, 1)
		// the old code still verifies through its own tokens
		c.Assert(csp.VerifyAuthToken(second, code), qt.IsNil)
	})

	c.Run("send ceiling reached", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 1)
		const email = "unsent-ceiling@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		_ = fetchOTPCodeFromEmail(c, email)

		c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, smsDest), qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		_, err = stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		c.Assert(testSMSService.ConsumeSMS(phone), qt.IsNil)
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Tokens, qt.HasLen, 1)
		c.Assert(data.Sends, qt.HasLen, 1)

		// past the cooldown the new code is refused by the ceiling itself
		csp.notificationCoolDownTime = time.Millisecond
		time.Sleep(10 * time.Millisecond)
		c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, smsDest), qt.ErrorIs, errors.ErrVerificationMaxAttempts)
		_, err = stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
		c.Assert(testSMSService.ConsumeSMS(phone), qt.IsNil)
		data, err = csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Tokens, qt.HasLen, 1)
	})
}

// assertNoEmailTo checks nothing was delivered to email; delivery is synchronous.
func assertNoEmailTo(c *qt.C, email string) {
	c.Helper()
	_, err := testMailService.FindEmail(context.Background(), email)
	c.Assert(err, qt.Not(qt.IsNil))
}

// TestClaimSendRereadsThroughRequestingToken checks that a refused send claim reports the refusal
// seen through the requesting token, not through the token the challenge was read through.
func TestClaimSendRereadsThroughRequestingToken(t *testing.T) {
	c := qt.New(t)
	const email = "reread@example.com"
	dest := DestinationKey(notifications.EmailChallenge, email)

	c.Run("read through a token since removed", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 0)
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		_ = fetchOTPCodeFromEmail(c, email)
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		challenge, err := csp.Storage.LastCSPAuth(testUserID, testAnchorID)
		c.Assert(err, qt.IsNil)
		c.Assert(challenge.Token, qt.DeepEquals, second)
		c.Assert(csp.Storage.RemoveCSPAuthToken(challenge.ID, second), qt.IsNil)

		_, err = csp.claimSend(challenge, first, dest)
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	})

	c.Run("read through a token that has since verified", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, testMailService, 0)
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		code := fetchOTPCodeFromEmail(c, email)
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		challenge, err := csp.Storage.LastCSPAuth(testUserID, testAnchorID)
		c.Assert(err, qt.IsNil)
		c.Assert(csp.VerifyAuthToken(second, code), qt.IsNil)

		_, err = csp.claimSend(challenge, first, dest)
		c.Assert(err, qt.ErrorIs, ErrTokenExpired)
		_, err = csp.claimSend(challenge, second, dest)
		c.Assert(err, qt.ErrorIs, errors.ErrUserAlreadyVerified)
	})
}

// TestReuseAfterConcurrentSendToDestination checks that a step 0 whose read of the code predates a
// concurrent send to the same destination returns its token instead of creating a second code.
func TestReuseAfterConcurrentSendToDestination(t *testing.T) {
	c := qt.New(t)
	const first, second = "race-first@example.com", "race-second@example.com"
	csp := newChallengeCSP(c, time.Hour, testMailService, 0)
	_, err := stepZero(csp, first, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	stale, err := csp.Storage.LastCSPAuth(testUserID, testAnchorID)
	c.Assert(err, qt.IsNil)
	// the concurrent request that wins: it sends the code to the second destination
	_, err = stepZero(csp, second, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	c.Assert(backoffSends(c, csp), qt.Equals, 2)

	token, err := csp.reuseChallenge(stale, challengeDelivery{
		to:    second,
		ctype: notifications.EmailChallenge,
		lang:  apicommon.DefaultLang,
		org:   notifications.OrganizationInfo{Address: testAddress.Address(), Name: testOrgName, Logo: testOrgLogo},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(token, qt.Not(qt.IsNil))
	last, err := csp.Storage.LastCSPAuth(testUserID, testAnchorID)
	c.Assert(err, qt.IsNil)
	c.Assert(last.ID, qt.DeepEquals, stale.ID)
	c.Assert(backoffSends(c, csp), qt.Equals, 2)
}

// TestChallengeAboutToExpireIsNotReused checks that a code with less than one cooldown left is not
// handed another token: step 0 takes the new-code path, cooldown first.
func TestChallengeAboutToExpireIsNotReused(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	dbName := test.RandomDatabaseName()
	testDB, err := db.New(testMongoURI, dbName)
	c.Assert(err, qt.IsNil)
	const coolDown, ttl = time.Hour, 2 * time.Hour
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: coolDown,
		NotificationTTL:          ttl,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)
	const email = "about-to-expire@example.com"
	dest := DestinationKey(notifications.EmailChallenge, email)
	first, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)
	data, err := csp.Storage.CSPAuth(first)
	c.Assert(err, qt.IsNil)

	tokens := testDB.DBClient.Database(dbName).Collection("cspTokens")
	aged := time.Now().Add(-(ttl - coolDown + time.Second))
	_, err = tokens.UpdateOne(ctx, bson.M{"_id": data.ID}, bson.M{"$set": bson.M{"createdat": aged}})
	c.Assert(err, qt.IsNil)
	// still inside the cooldown of its send: refused where reuse would have handed out a token
	c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, dest), qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	_, err = stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)

	// once its send is past the cooldown, a new code replaces it
	_, err = tokens.UpdateOne(ctx, bson.M{"_id": data.ID}, bson.M{"$set": bson.M{"sends." + dest: aged}})
	c.Assert(err, qt.IsNil)
	second, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)
	secondData, err := csp.Storage.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.ID, qt.Not(qt.DeepEquals), data.ID)
	firstData, err := csp.Storage.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(firstData.Tokens, qt.HasLen, 1)
}
