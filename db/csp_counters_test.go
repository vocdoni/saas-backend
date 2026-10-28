package db

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestCSPCounterWindows(t *testing.T) {
	c := qt.New(t)
	key := []byte("counter-key")
	// 2026-10-09 23:42:10 at UTC+2 is 21:42:10 UTC
	now := time.Date(2026, 10, 9, 23, 42, 10, 0, time.FixedZone("CEST", 2*60*60))

	// failed authentications use hourly windows, aligned to the UTC hour
	c.Assert(CSPCounterWindow(CSPCounterAuthFailures), qt.Equals, time.Hour)
	auth := currentCSPCounterBucket(CSPCounterAuthFailures, key, now)
	c.Assert(auth.resetAt.Equal(time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)), qt.IsTrue)
	c.Assert(auth.expiry.Equal(time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)), qt.IsTrue)
	// the same hour shares a bucket, the next one does not
	c.Assert(currentCSPCounterBucket(CSPCounterAuthFailures, key, now.Add(17*time.Minute)).id,
		qt.DeepEquals, auth.id)
	c.Assert(currentCSPCounterBucket(CSPCounterAuthFailures, key, now.Add(18*time.Minute)).id,
		qt.Not(qt.DeepEquals), auth.id)

	// challenge sends stay daily, aligned to midnight UTC
	c.Assert(CSPCounterWindow(CSPCounterChallengeSends), qt.Equals, 24*time.Hour)
	sends := currentCSPCounterBucket(CSPCounterChallengeSends, key, now)
	c.Assert(sends.resetAt.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)), qt.IsTrue)
	c.Assert(sends.expiry.Equal(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)), qt.IsTrue)
	c.Assert(currentCSPCounterBucket(CSPCounterChallengeSends, key, now.Add(2*time.Hour)).id,
		qt.DeepEquals, sends.id)
	c.Assert(currentCSPCounterBucket(CSPCounterChallengeSends, key, now.Add(2*time.Hour+18*time.Minute)).id,
		qt.Not(qt.DeepEquals), sends.id)
}

func TestClaimCSPCounter(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	key := []byte("counter-key")
	claim := func(kind string, maxCount int) CSPCounterResult {
		res, err := testDB.ClaimCSPCounter(kind, key, maxCount)
		c.Assert(err, qt.IsNil)
		return res
	}

	c.Run("bad inputs", func(c *qt.C) {
		_, err := testDB.ClaimCSPCounter("", key, 3)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.ClaimCSPCounter(CSPCounterChallengeSends, nil, 3)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.CSPCounterReached("", key, 3)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.CSPCounterReached(CSPCounterChallengeSends, nil, 3)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
	})

	c.Run("claims up to max then refuses until the window resets", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		const maxClaims = 3
		for range maxClaims {
			c.Assert(claim(CSPCounterChallengeSends, maxClaims).Granted, qt.IsTrue)
		}
		// the cap is reached: no more claims in this window, and the refusal says when it resets
		refused := claim(CSPCounterChallengeSends, maxClaims)
		c.Assert(refused.Granted, qt.IsFalse)
		c.Assert(refused.Claim, qt.HasLen, 0)
		c.Assert(refused.ResetAt.After(time.Now()), qt.IsTrue)
		c.Assert(time.Until(refused.ResetAt) <= 24*time.Hour, qt.IsTrue)
		reached, err := testDB.CSPCounterReached(CSPCounterChallengeSends, key, maxClaims)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsTrue)
		// a higher max still has budget left
		reached, err = testDB.CSPCounterReached(CSPCounterChallengeSends, key, maxClaims+1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
	})

	c.Run("kinds and keys are independent", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		c.Assert(claim(CSPCounterChallengeSends, 1).Granted, qt.IsTrue)
		// same key, other kind: untouched
		reached, err := testDB.CSPCounterReached(CSPCounterAuthFailures, key, 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
		// same kind, other key: untouched
		reached, err = testDB.CSPCounterReached(CSPCounterChallengeSends, []byte("other-key"), 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
	})

	c.Run("concurrent first claims are all granted up to max", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// two service replicas share the database: use a second handle so the claims really race
		// on the window's document creation
		other, err := New(mongoURI, testDB.database)
		c.Assert(err, qt.IsNil)
		const claimers = 16
		const maxClaims = claimers / 2
		var granted atomic.Int32
		var wg sync.WaitGroup
		for i := range claimers {
			wg.Go(func() {
				storage := testDB
				if i%2 == 1 {
					storage = other
				}
				res, err := storage.ClaimCSPCounter(CSPCounterChallengeSends, key, maxClaims)
				c.Check(err, qt.IsNil)
				if res.Granted {
					granted.Add(1)
				}
			})
		}
		wg.Wait()
		// a duplicate key from a raced first upsert is not a reached cap: exactly max are granted
		c.Assert(int(granted.Load()), qt.Equals, maxClaims)
	})

	c.Run("release gives a claimed unit back", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		first := claim(CSPCounterAuthFailures, 1)
		c.Assert(first.Granted, qt.IsTrue)
		c.Assert(first.Claim, qt.Not(qt.HasLen), 0)
		// an hourly window: it resets within the hour
		c.Assert(time.Until(first.ResetAt) <= time.Hour, qt.IsTrue)
		c.Assert(claim(CSPCounterAuthFailures, 1).Granted, qt.IsFalse)
		// releasing frees the slot again
		c.Assert(testDB.ReleaseCSPCounter(first.Claim), qt.IsNil)
		reached, err := testDB.CSPCounterReached(CSPCounterAuthFailures, key, 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
		// releasing more than was claimed never goes below zero, so it cannot mint extra budget
		c.Assert(testDB.ReleaseCSPCounter(first.Claim), qt.IsNil)
		c.Assert(claim(CSPCounterAuthFailures, 1).Granted, qt.IsTrue)
		c.Assert(claim(CSPCounterAuthFailures, 1).Granted, qt.IsFalse)
		// a disabled counter's empty claim is a no-op
		c.Assert(testDB.ReleaseCSPCounter(nil), qt.IsNil)
	})

	c.Run("non-positive max disables the counter", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		for _, limit := range []int{0, -1} {
			res := claim(CSPCounterChallengeSends, limit)
			c.Assert(res.Granted, qt.IsTrue)
			c.Assert(res.Claim, qt.HasLen, 0)
			reached, err := testDB.CSPCounterReached(CSPCounterChallengeSends, key, limit)
			c.Assert(err, qt.IsNil)
			c.Assert(reached, qt.IsFalse)
		}
		// nothing was written while disabled
		reached, err := testDB.CSPCounterReached(CSPCounterChallengeSends, key, 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
	})
}
