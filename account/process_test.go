package account

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestElectionStartDuration(t *testing.T) {
	c := qt.New(t)
	now := time.Now()

	// endDate is required
	_, _, err := electionStartDuration(time.Time{}, time.Time{})
	c.Assert(err, qt.ErrorMatches, "endDate is required")

	// future start with a fixed window: scheduled start, exact span
	start := now.Add(time.Hour)
	end := start.Add(2 * time.Hour)
	st, dur, err := electionStartDuration(start, end)
	c.Assert(err, qt.IsNil)
	c.Assert(st, qt.Equals, uint32(start.Unix()))
	c.Assert(dur, qt.Equals, uint32(7200))

	// past start: start immediately (st=0) and end at endDate, so the duration is
	// measured from now (~1h) rather than from startDate (~2h). This is the regression.
	st, dur, err = electionStartDuration(now.Add(-time.Hour), now.Add(time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(st, qt.Equals, uint32(0))
	c.Assert(dur > 3590 && dur <= 3600, qt.IsTrue, qt.Commentf("dur=%d (want ~3600, not 7200)", dur))

	// endDate not after startDate
	_, _, err = electionStartDuration(start, start)
	c.Assert(err, qt.ErrorMatches, "endDate must be after startDate")

	// both dates in the past
	_, _, err = electionStartDuration(now.Add(-2*time.Hour), now.Add(-time.Hour))
	c.Assert(err, qt.ErrorMatches, "endDate must be in the future")

	// only endDate, in the future
	st, dur, err = electionStartDuration(time.Time{}, now.Add(time.Hour))
	c.Assert(err, qt.IsNil)
	c.Assert(st, qt.Equals, uint32(0))
	c.Assert(dur > 3590 && dur <= 3600, qt.IsTrue, qt.Commentf("dur=%d", dur))

	// only endDate, in the past
	_, _, err = electionStartDuration(time.Time{}, now.Add(-time.Hour))
	c.Assert(err, qt.ErrorMatches, "endDate must be in the future")
}
