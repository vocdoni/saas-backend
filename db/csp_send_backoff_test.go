package db

import (
	"context"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCSPSendBackoffPolicyWait(t *testing.T) {
	c := qt.New(t)
	p := CSPSendBackoffPolicy{BaseWait: time.Minute, MaxWait: time.Hour, FailuresPerLevel: 5}
	for k, want := range map[int]time.Duration{
		0: 0, 3: 0, 4: 2 * time.Minute, 5: 4 * time.Minute, 8: 32 * time.Minute, 9: time.Hour, 1000: time.Hour,
	} {
		c.Assert(p.wait(k), qt.Equals, want, qt.Commentf("level %d", k))
	}
	// a large base is capped without overflowing
	big := CSPSendBackoffPolicy{BaseWait: 24 * time.Hour, MaxWait: 48 * time.Hour}
	c.Assert(big.wait(1000), qt.Equals, 48*time.Hour)
	// no max wait, no wait
	c.Assert(CSPSendBackoffPolicy{BaseWait: time.Minute}.wait(10), qt.Equals, time.Duration(0))
	// every FailuresPerLevel failures add a level
	c.Assert(p.level(3, 4), qt.Equals, 3)
	c.Assert(p.level(3, 5), qt.Equals, 4)
	c.Assert(p.level(3, 14), qt.Equals, 5)

	now := time.Now()
	c.Assert((*CSPSendBackoff)(nil).Refusal(p, now).Refused(), qt.IsFalse)
	b := &CSPSendBackoff{Level: 5, LastSend: now.Add(-time.Minute), Sends: []time.Time{now.Add(-time.Minute)}}
	p.QuietPeriod = time.Hour
	c.Assert(b.Refusal(p, now).WaitUntil.Equal(now.Add(3*time.Minute)), qt.IsTrue)
	// after the quiet period the level no longer counts
	p.QuietPeriod = 30 * time.Second
	c.Assert(b.Refusal(p, now).Refused(), qt.IsFalse)
}

func TestClaimCSPSend(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	key := []byte("backoff-key")
	noWait := CSPSendBackoffPolicy{Ceiling: 3, BaseWait: time.Minute, QuietPeriod: time.Hour, FailuresPerLevel: 5}

	c.Run("bad inputs", func(c *qt.C) {
		_, _, err := testDB.ClaimCSPSend(nil, noWait)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		_, err = testDB.CSPSendBackoff(nil)
		c.Assert(err, qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.AddCSPSendFailure(nil), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ResetCSPSendBackoff(nil), qt.ErrorIs, ErrBadInputs)
		c.Assert(testDB.ReleaseCSPSend(CSPSendBackoffClaim{}), qt.IsNil)
		b, err := testDB.CSPSendBackoff(key)
		c.Assert(err, qt.IsNil)
		c.Assert(b, qt.IsNil)
	})

	c.Run("claims store unique times up to the ceiling", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		var claims []CSPSendBackoffClaim
		for range 3 {
			claim, refusal, err := testDB.ClaimCSPSend(key, noWait)
			c.Assert(err, qt.IsNil)
			c.Assert(refusal.Refused(), qt.IsFalse)
			if n := len(claims); n > 0 {
				c.Assert(claim.At.After(claims[n-1].At), qt.IsTrue)
			}
			claims = append(claims, claim)
		}
		claim, refusal, err := testDB.ClaimCSPSend(key, noWait)
		c.Assert(err, qt.IsNil)
		c.Assert(claim, qt.DeepEquals, CSPSendBackoffClaim{})
		c.Assert(refusal.CeilingFreeAt.Equal(claims[0].At.Add(CSPSendWindow)), qt.IsTrue)
		b, err := testDB.CSPSendBackoff(key)
		c.Assert(err, qt.IsNil)
		c.Assert(b.Sends, qt.HasLen, 3)
		c.Assert(b.Level, qt.Equals, 3)
		c.Assert(b.LastSend.Equal(claims[2].At), qt.IsTrue)
		c.Assert(b.ExpiresAt.Equal(claims[2].At.Add(CSPSendWindow)), qt.IsTrue)

		// a release only matches its own send, once
		c.Assert(testDB.ReleaseCSPSend(CSPSendBackoffClaim{ID: claims[0].ID, At: time.Now().Add(time.Hour)}), qt.IsNil)
		c.Assert(testDB.ReleaseCSPSend(claims[1]), qt.IsNil)
		c.Assert(testDB.ReleaseCSPSend(claims[1]), qt.IsNil)
		b, err = testDB.CSPSendBackoff(key)
		c.Assert(err, qt.IsNil)
		c.Assert(b.Sends, qt.HasLen, 2)
		c.Assert(b.Sends[0].Equal(claims[0].At), qt.IsTrue)
		c.Assert(b.Sends[1].Equal(claims[2].At), qt.IsTrue)
		c.Assert(b.Level, qt.Equals, 2)
		c.Assert(b.LastSend.Equal(claims[2].At), qt.IsTrue)
		// the freed send can be claimed again
		_, refusal, err = testDB.ClaimCSPSend(key, noWait)
		c.Assert(err, qt.IsNil)
		c.Assert(refusal.Refused(), qt.IsFalse)
	})

	c.Run("the wait refuses until it has passed", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		p := CSPSendBackoffPolicy{BaseWait: time.Minute, MaxWait: time.Hour, QuietPeriod: time.Hour, FailuresPerLevel: 5}
		for range 4 {
			_, refusal, err := testDB.ClaimCSPSend(key, p)
			c.Assert(err, qt.IsNil)
			c.Assert(refusal.Refused(), qt.IsFalse)
		}
		b, err := testDB.CSPSendBackoff(key)
		c.Assert(err, qt.IsNil)
		_, refusal, err := testDB.ClaimCSPSend(key, p)
		c.Assert(err, qt.IsNil)
		c.Assert(refusal.WaitUntil.Equal(b.LastSend.Add(2*time.Minute)), qt.IsTrue)
		c.Assert(refusal.CeilingFreeAt.IsZero(), qt.IsTrue)
		// five failures add a level
		for range 5 {
			c.Assert(testDB.AddCSPSendFailure(key), qt.IsNil)
		}
		_, refusal, err = testDB.ClaimCSPSend(key, p)
		c.Assert(err, qt.IsNil)
		c.Assert(refusal.WaitUntil.Equal(b.LastSend.Add(4*time.Minute)), qt.IsTrue)
		// once the wait has passed, the claim goes through on the server too
		_, err = testDB.cspSendBackoff.UpdateOne(context.Background(), bson.M{"_id": cspSendBackoffID(key)},
			bson.M{"$set": bson.M{"lastsend": time.Now().Add(-4*time.Minute - time.Second)}})
		c.Assert(err, qt.IsNil)
		_, refusal, err = testDB.ClaimCSPSend(key, p)
		c.Assert(err, qt.IsNil)
		c.Assert(refusal.Refused(), qt.IsFalse)
		// a reset clears level and failures but keeps the sends
		c.Assert(testDB.ResetCSPSendBackoff(key), qt.IsNil)
		b, err = testDB.CSPSendBackoff(key)
		c.Assert(err, qt.IsNil)
		c.Assert(b.Level, qt.Equals, 0)
		c.Assert(b.Failures, qt.Equals, 0)
		c.Assert(b.Sends, qt.HasLen, 5)
	})

	c.Run("concurrent claims stop at the ceiling", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		// two service replicas share the database: a second handle makes the claims really race
		other, err := New(mongoURI, testDB.database)
		c.Assert(err, qt.IsNil)
		p := noWait
		p.Ceiling = 5
		const claimers = 16
		var mu sync.Mutex
		var granted []time.Time
		var wg sync.WaitGroup
		for i := range claimers {
			wg.Go(func() {
				storage := testDB
				if i%2 == 1 {
					storage = other
				}
				claim, refusal, err := storage.ClaimCSPSend(key, p)
				c.Check(err, qt.IsNil)
				if !refusal.Refused() && err == nil {
					mu.Lock()
					granted = append(granted, claim.At)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		c.Assert(granted, qt.HasLen, p.Ceiling)
		seen := map[int64]bool{}
		for _, at := range granted {
			c.Assert(seen[at.UnixMilli()], qt.IsFalse)
			seen[at.UnixMilli()] = true
		}
	})

	c.Run("concurrent claims inside a wait grant one", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		other, err := New(mongoURI, testDB.database)
		c.Assert(err, qt.IsNil)
		p := CSPSendBackoffPolicy{BaseWait: time.Minute, MaxWait: time.Hour, QuietPeriod: time.Hour, FailuresPerLevel: 5}
		for range 4 {
			_, _, err := testDB.ClaimCSPSend(key, p)
			c.Assert(err, qt.IsNil)
		}
		// the 2 min wait after the fourth send has just passed
		_, err = testDB.cspSendBackoff.UpdateOne(context.Background(), bson.M{"_id": cspSendBackoffID(key)},
			bson.M{"$set": bson.M{"lastsend": time.Now().Add(-2*time.Minute - time.Second)}})
		c.Assert(err, qt.IsNil)
		var mu sync.Mutex
		granted := 0
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				storage := testDB
				if i%2 == 1 {
					storage = other
				}
				_, refusal, err := storage.ClaimCSPSend(key, p)
				c.Check(err, qt.IsNil)
				if err == nil && !refusal.Refused() {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		c.Assert(granted, qt.Equals, 1)
	})
}
