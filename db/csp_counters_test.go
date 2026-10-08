package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestClaimCSPCounter(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	key := []byte("counter-key")

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

	c.Run("claims up to max then refuses", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		const maxClaims = 3
		for range maxClaims {
			claimed, err := testDB.ClaimCSPCounter(CSPCounterChallengeSends, key, maxClaims)
			c.Assert(err, qt.IsNil)
			c.Assert(claimed, qt.IsTrue)
		}
		// the cap is reached: no more claims today
		claimed, err := testDB.ClaimCSPCounter(CSPCounterChallengeSends, key, maxClaims)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsFalse)
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
		claimed, err := testDB.ClaimCSPCounter(CSPCounterChallengeSends, key, 1)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsTrue)
		// same key, other kind: untouched
		reached, err := testDB.CSPCounterReached(CSPCounterAuthFailureMember, key, 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
		// same kind, other key: untouched
		reached, err = testDB.CSPCounterReached(CSPCounterChallengeSends, []byte("other-key"), 1)
		c.Assert(err, qt.IsNil)
		c.Assert(reached, qt.IsFalse)
	})

	c.Run("non-positive max disables the counter", func(c *qt.C) {
		c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
		for _, limit := range []int{0, -1} {
			claimed, err := testDB.ClaimCSPCounter(CSPCounterChallengeSends, key, limit)
			c.Assert(err, qt.IsNil)
			c.Assert(claimed, qt.IsTrue)
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
