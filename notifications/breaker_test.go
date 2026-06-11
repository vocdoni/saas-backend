package notifications

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestBreaker(t *testing.T) {
	c := qt.New(t)

	now := time.Unix(1000, 0)
	b := NewBreaker("test", 3, time.Minute)
	b.now = func() time.Time { return now }

	// starts closed
	c.Assert(b.Allow(), qt.IsTrue)
	c.Assert(b.RetryAfter(), qt.Equals, time.Duration(0))

	// failures below threshold keep it closed
	b.RecordFailure()
	b.RecordFailure()
	c.Assert(b.Allow(), qt.IsTrue)

	// reaching the threshold opens it for the cooldown
	b.RecordFailure()
	c.Assert(b.Allow(), qt.IsFalse)
	c.Assert(b.RetryAfter() > 0, qt.IsTrue)

	// still open just before the cooldown elapses
	now = now.Add(time.Minute - time.Second)
	c.Assert(b.Allow(), qt.IsFalse)

	// half-open once the cooldown has elapsed
	now = now.Add(2 * time.Second)
	c.Assert(b.Allow(), qt.IsTrue)
	c.Assert(b.RetryAfter(), qt.Equals, time.Duration(0))

	// a failed probe reopens it
	b.RecordFailure()
	c.Assert(b.Allow(), qt.IsFalse)

	// a success closes it and resets the failure count
	now = now.Add(time.Minute + time.Second)
	b.RecordSuccess()
	c.Assert(b.Allow(), qt.IsTrue)
	b.RecordFailure()
	b.RecordFailure()
	c.Assert(b.Allow(), qt.IsTrue) // only 2 failures after reset, threshold is 3
}

func TestBreakerDefaults(t *testing.T) {
	c := qt.New(t)
	b := NewBreaker("test", 0, 0)
	c.Assert(b.maxFailures, qt.Equals, DefaultBreakerMaxFailures)
	c.Assert(b.cooldown, qt.Equals, DefaultBreakerCooldown)
}
