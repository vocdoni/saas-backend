package csp

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// backoffBase is the notification cooldown of newBackoffCSP, the base of the backoff's waits.
const backoffBase = time.Minute

// newBackoffCSP returns a CSP on a fresh database with synchronous delivery, a cooldown of
// backoffBase, codes living two hours, the given max wait (also the quiet period) and send
// ceiling, and the collection holding its send backoffs, for direct edits.
func newBackoffCSP(c *qt.C, maxWait time.Duration, ceiling int) (*CSP, *mongo.Collection) {
	c.Helper()
	dbName := test.RandomDatabaseName()
	testDB, err := db.New(testMongoURI, dbName)
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: backoffBase,
		NotificationTTL:          2 * time.Hour,
		MaxDailyChallengeSends:   ceiling,
		MaxSendBackoff:           maxWait,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)
	return csp, testDB.DBClient.Database(dbName).Collection("cspSendBackoff")
}

// claimTestSend claims a send in the test user's backoff on the test anchor.
func claimTestSend(csp *CSP) (db.CSPSendBackoffClaim, error) {
	return csp.claimVoterSend(testUserID, testAnchorID)
}

// testBackoff reads the test user's backoff on the test anchor.
func testBackoff(c *qt.C, csp *CSP) *db.CSPSendBackoff {
	c.Helper()
	backoff, err := csp.Storage.CSPSendBackoff(counterKey(testUserID, testAnchorID))
	c.Assert(err, qt.IsNil)
	c.Assert(backoff, qt.Not(qt.IsNil))
	return backoff
}

// ageBackoffs moves every time stored in the backoffs of coll d into the past, as if d had passed.
func ageBackoffs(c *qt.C, coll *mongo.Collection, d time.Duration) {
	c.Helper()
	shift := func(field string) bson.M {
		return bson.M{"$cond": bson.A{
			bson.M{"$eq": bson.A{bson.M{"$type": "$" + field}, "missing"}},
			"$$REMOVE",
			bson.M{"$subtract": bson.A{"$" + field, d.Milliseconds()}},
		}}
	}
	_, err := coll.UpdateMany(context.Background(), bson.M{}, mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"sends":    bson.M{"$map": bson.M{"input": "$sends", "as": "s", "in": bson.M{"$subtract": bson.A{"$$s", d.Milliseconds()}}}},
		"lastsend": shift("lastsend"),
		"resetat":  shift("resetat"),
	}}}})
	c.Assert(err, qt.IsNil)
}

// assertCoolDownNear checks that err is a 40103 whose coolDownTime is want, give or take the
// seconds a test takes.
func assertCoolDownNear(c *qt.C, err error, want time.Duration) {
	c.Helper()
	c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
	var apiErr errors.Error
	c.Assert(errors.As(err, &apiErr), qt.IsTrue)
	data, ok := apiErr.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", apiErr.Data))
	ms, ok := data["coolDownTime"].(int64)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", apiErr.Data))
	assertNear(c, time.Duration(ms)*time.Millisecond, want)
}

// assertNear checks that got is want, give or take the seconds a test takes.
func assertNear(c *qt.C, got, want time.Duration) {
	c.Helper()
	c.Assert(got <= want+time.Second, qt.IsTrue, qt.Commentf("got %s, want %s", got, want))
	c.Assert(got > want-10*time.Second, qt.IsTrue, qt.Commentf("got %s, want %s", got, want))
}

// TestSendBackoffGrows checks the voter-level wait: none after the first three sends, then twice
// the base doubling per send up to the max wait, and back to none after a quiet period.
func TestSendBackoffGrows(t *testing.T) {
	c := qt.New(t)
	const maxWait = 10 * time.Minute
	csp, coll := newBackoffCSP(c, maxWait, 0)

	// up to three sends wait nothing, so the fourth goes out at once
	for range 4 {
		_, err := claimTestSend(csp)
		c.Assert(err, qt.IsNil)
	}
	// from the fourth send the wait doubles from twice the base
	for _, wait := range []time.Duration{2 * backoffBase, 4 * backoffBase, 8 * backoffBase} {
		_, err := claimTestSend(csp)
		assertCoolDownNear(c, err, wait)
		ageBackoffs(c, coll, wait+time.Second)
		_, err = claimTestSend(csp)
		c.Assert(err, qt.IsNil)
	}
	// 16 times the base is capped at the max wait
	_, err := claimTestSend(csp)
	assertCoolDownNear(c, err, maxWait)
	c.Assert(testBackoff(c, csp).Level, qt.Equals, 7)

	// a quiet period (the max wait by default) resets the level: the send counts as the first
	ageBackoffs(c, coll, maxWait+time.Second)
	_, err = claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	backoff := testBackoff(c, csp)
	c.Assert(backoff.Level, qt.Equals, 1)
	// the rolling window keeps every send
	c.Assert(backoff.Sends, qt.HasLen, 8)
	_, err = claimTestSend(csp)
	c.Assert(err, qt.IsNil)
}

// TestSendBackoffNormalVoter checks, through AuthToken and resends, that three sends only see the
// per-destination cooldown, and that past them a new code is refused by the voter-level wait, which
// the 40103, the certificate peek and the resend hint all carry.
func TestSendBackoffNormalVoter(t *testing.T) {
	c := qt.New(t)
	csp, _ := newBackoffCSP(c, time.Hour, 0)
	const emailA, emailB, emailC, emailD = "normal-a@example.com", "normal-b@example.com",
		"normal-c@example.com", "normal-d@example.com"
	token, err := stepZero(csp, emailA, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailA)
	c.Assert(resendTo(csp, token, emailB, notifications.EmailChallenge), qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailB)
	c.Assert(resendTo(csp, token, emailC, notifications.EmailChallenge), qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailC)

	// after three sends only the destination's own cooldown applies
	assertCoolDownNear(c, resendTo(csp, token, emailC, notifications.EmailChallenge), backoffBase)
	remaining, ok := csp.ResendCoolDown(token, DestinationKey(notifications.EmailChallenge, emailD))
	c.Assert(ok, qt.IsTrue)
	c.Assert(remaining, qt.Equals, time.Duration(0))
	c.Assert(resendTo(csp, token, emailD, notifications.EmailChallenge), qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailD)

	// the fourth send starts a wait of twice the base: the code's resends are spent, so a fifth
	// contact needs a new code, refused with the longer of the code's cooldown and that wait
	const emailE = "normal-e@example.com"
	destE := DestinationKey(notifications.EmailChallenge, emailE)
	assertCoolDownNear(c, csp.ChallengeSendRefusal(testUserID, testAnchorID, destE), 2*backoffBase)
	_, err = stepZero(csp, emailE, notifications.EmailChallenge)
	assertCoolDownNear(c, err, 2*backoffBase)
	assertNoEmailTo(c, emailE)
}

// TestSendBackoffHint checks that the resend hint reports the voter-level wait when it is longer
// than the destination's cooldown, and no resend at the ceiling.
func TestSendBackoffHint(t *testing.T) {
	c := qt.New(t)
	csp, _ := newBackoffCSP(c, time.Hour, 5)
	const email = "hint-backoff@example.com"
	dest := DestinationKey(notifications.EmailChallenge, email)
	other := DestinationKey(notifications.SMSChallenge, "+34600000077")
	token, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)
	remaining, ok := csp.ResendCoolDown(token, dest)
	c.Assert(ok, qt.IsTrue)
	assertNear(c, remaining, backoffBase)

	for range 3 {
		_, err := claimTestSend(csp)
		c.Assert(err, qt.IsNil)
	}
	for _, d := range []string{dest, other} {
		remaining, ok := csp.ResendCoolDown(token, d)
		c.Assert(ok, qt.IsTrue)
		assertNear(c, remaining, 2*backoffBase)
	}
	_, err = claimTestSend(csp)
	assertCoolDownNear(c, err, 2*backoffBase)

	// a fifth send reaches the ceiling: no resend is advertised
	csp.maxSendBackoff = -1
	_, err = claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	_, ok = csp.ResendCoolDown(token, other)
	c.Assert(ok, qt.IsFalse)
}

// TestSendBackoffVerificationResets checks that a successful verification resets the level and the
// failures, but not the sends of the rolling window.
func TestSendBackoffVerificationResets(t *testing.T) {
	c := qt.New(t)
	csp, _ := newBackoffCSP(c, time.Hour, 0)
	const email = "backoff-verify@example.com"
	token, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	code := fetchOTPCodeFromEmail(c, email)
	for range 3 {
		_, err := claimTestSend(csp)
		c.Assert(err, qt.IsNil)
	}
	c.Assert(csp.VerifyAuthToken(token, "000000x"), qt.ErrorIs, ErrChallengeCodeFailure)
	backoff := testBackoff(c, csp)
	c.Assert(backoff.Level, qt.Equals, 4)
	c.Assert(backoff.Failures, qt.Equals, 1)
	_, err = claimTestSend(csp)
	assertCoolDownNear(c, err, 2*backoffBase)

	c.Assert(csp.VerifyAuthToken(token, code), qt.IsNil)
	backoff = testBackoff(c, csp)
	c.Assert(backoff.Level, qt.Equals, 0)
	c.Assert(backoff.Failures, qt.Equals, 0)
	c.Assert(backoff.Sends, qt.HasLen, 4)
	_, err = claimTestSend(csp)
	c.Assert(err, qt.IsNil)
}

// TestSendBackoffFailuresAcrossCodes checks that every MaxChallengeAttempts failed attempts raise
// the backoff level by one, counted across codes, so a new code brings no fresh free guesses.
func TestSendBackoffFailuresAcrossCodes(t *testing.T) {
	c := qt.New(t)
	csp, _ := newBackoffCSP(c, time.Hour, 0)
	const email = "backoff-failures@example.com"
	refusal := func() db.CSPSendRefusal {
		r, err := csp.voterSendRefusal(testUserID, testAnchorID)
		c.Assert(err, qt.IsNil)
		return r
	}

	// code A: one send, locked by its five failed attempts
	tokenA, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)
	for range MaxChallengeAttempts {
		c.Assert(csp.VerifyAuthToken(tokenA, "000000x"), qt.Not(qt.IsNil))
	}
	// attempts on a locked code are refused before counting
	c.Assert(csp.VerifyAuthToken(tokenA, "000000x"), qt.ErrorIs, ErrTooManyAttempts)
	c.Assert(testBackoff(c, csp).Failures, qt.Equals, MaxChallengeAttempts)

	// code B, once A's cooldown has passed: level 2 plus one for A's failures, still no wait
	csp.notificationCoolDownTime = time.Millisecond
	time.Sleep(10 * time.Millisecond)
	tokenB, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)
	csp.notificationCoolDownTime = backoffBase
	c.Assert(refusal().Refused(), qt.IsFalse)
	for range MaxChallengeAttempts - 1 {
		c.Assert(csp.VerifyAuthToken(tokenB, "000000x"), qt.ErrorIs, ErrChallengeCodeFailure)
	}
	c.Assert(refusal().Refused(), qt.IsFalse)
	// the fifth failure on B adds a level: twice the base from B's send
	c.Assert(csp.VerifyAuthToken(tokenB, "000000x"), qt.Not(qt.IsNil))
	backoff := testBackoff(c, csp)
	c.Assert(backoff.Failures, qt.Equals, 2*MaxChallengeAttempts)
	c.Assert(refusal().WaitUntil.Equal(backoff.LastSend.Add(2*backoffBase)), qt.IsTrue)
}

// TestSendBackoffCeiling checks the rolling 24 h ceiling: refused with 40017 and the seconds until
// the oldest send ages out, freed as sends age out, and answered alike by AuthToken and the peek.
func TestSendBackoffCeiling(t *testing.T) {
	c := qt.New(t)
	csp, coll := newBackoffCSP(c, -1, 3)
	for range 3 {
		_, err := claimTestSend(csp)
		c.Assert(err, qt.IsNil)
	}
	_, err := claimTestSend(csp)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	assertRetryAfter(c, err, 24*time.Hour)
	assertRetryAfterAtLeast(c, err, 24*time.Hour-time.Minute)

	// the oldest send turns 24 h old: one send frees, and the next refusal waits for the second
	_, err = coll.UpdateOne(context.Background(), bson.M{},
		bson.M{"$set": bson.M{"sends.0": time.Now().Add(-24*time.Hour - time.Second)}})
	c.Assert(err, qt.IsNil)
	_, err = claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	c.Assert(testBackoff(c, csp).Sends, qt.HasLen, 3)
	ageBackoffs(c, coll, 23*time.Hour)
	_, err = claimTestSend(csp)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	assertRetryAfter(c, err, time.Hour)
	assertRetryAfterAtLeast(c, err, time.Hour-time.Minute)

	// AuthToken and the certificate peek answer the same refusal for a new code
	const email = "backoff-ceiling@example.com"
	dest := DestinationKey(notifications.EmailChallenge, email)
	c.Assert(csp.ChallengeSendRefusal(testUserID, testAnchorID, dest), qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	_, err = stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.ErrorIs, errors.ErrVerificationMaxAttempts)
	assertNoEmailTo(c, email)
}

// TestSendBackoffRelease checks that a released send gives its budget back exactly once, never
// undoes a later send, and leaves a level reset after it alone.
func TestSendBackoffRelease(t *testing.T) {
	c := qt.New(t)
	csp, _ := newBackoffCSP(c, time.Hour, 0)
	first, err := claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	second, err := claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	c.Assert(second.At.After(first.At), qt.IsTrue)

	csp.releaseVoterSend(first)
	csp.releaseVoterSend(first)
	backoff := testBackoff(c, csp)
	c.Assert(backoff.Sends, qt.HasLen, 1)
	c.Assert(backoff.Sends[0].Equal(second.At), qt.IsTrue)
	c.Assert(backoff.Level, qt.Equals, 1)
	c.Assert(backoff.LastSend.Equal(second.At), qt.IsTrue)

	csp.releaseVoterSend(second)
	backoff = testBackoff(c, csp)
	c.Assert(backoff.Sends, qt.HasLen, 0)
	c.Assert(backoff.Level, qt.Equals, 0)
	c.Assert(backoff.LastSend.IsZero(), qt.IsTrue)
	// a zero claim (nothing claimed) is a no-op
	csp.releaseVoterSend(db.CSPSendBackoffClaim{})

	// a send from before a reset does not count in the fresh level
	before, err := claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	c.Assert(csp.Storage.ResetCSPSendBackoff(counterKey(testUserID, testAnchorID)), qt.IsNil)
	after, err := claimTestSend(csp)
	c.Assert(err, qt.IsNil)
	csp.releaseVoterSend(before)
	backoff = testBackoff(c, csp)
	c.Assert(backoff.Level, qt.Equals, 1)
	c.Assert(backoff.Sends, qt.HasLen, 1)
	c.Assert(backoff.Sends[0].Equal(after.At), qt.IsTrue)
}

// TestSendBackoffRollbackThroughQueue checks that a send no provider is called for gives its
// backoff send and level back through the queue's result.
func TestSendBackoffRollbackThroughQueue(t *testing.T) {
	c := qt.New(t)
	// no mail service: the queue gives every email up without a provider call
	csp := newChallengeCSP(c, time.Hour, nil, 0)
	const email = "backoff-rollback@example.com"
	_, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	eventually(c, func() bool { return backoffSends(c, csp) == 0 })
	backoff := testBackoff(c, csp)
	c.Assert(backoff.Level, qt.Equals, 0)
	c.Assert(backoff.LastSend.IsZero(), qt.IsTrue)
}
