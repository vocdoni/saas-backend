package account

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/proto/build/go/models"
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

// TestBuildNewProcessTxInitialStatus covers the InitialStatus knob on BuildNewProcessTx:
// the default (unset) resolves to READY (historical behaviour), an explicit PAUSED is
// honoured and forces Mode.Interruptible on so the admin can later unpause, and any
// status outside the vochain's NewProcess whitelist is rejected without a chain call.
//
// The tests pass an explicit Nonce so BuildNewProcessTx skips the client account lookup
// and no *apiclient.HTTPclient is needed on the receiver.
func TestBuildNewProcessTxInitialStatus(t *testing.T) {
	baseParams := func() *db.ElectionParams {
		return &db.ElectionParams{
			EndDate:       time.Now().Add(time.Hour),
			MaxCensusSize: 10,
			ElectionType:  db.ElectionType{Interruptible: false},
		}
	}
	newParams := func(status models.ProcessStatus, interruptible bool) *NewProcessParams {
		ep := baseParams()
		ep.ElectionType.Interruptible = interruptible
		nonce := uint32(0)
		return &NewProcessParams{
			Params:        ep,
			CensusRoot:    []byte{0x01},
			CensusURI:     "https://example.invalid",
			MetadataURL:   "https://example.invalid/meta.json",
			Nonce:         &nonce,
			InitialStatus: status,
		}
	}
	a := &Account{} // client is not touched when Nonce is set

	t.Run("default resolves to READY", func(t *testing.T) {
		c := qt.New(t)
		tx, err := a.BuildNewProcessTx(newParams(models.ProcessStatus_PROCESS_UNKNOWN, false))
		c.Assert(err, qt.IsNil)
		p := tx.GetNewProcess().GetProcess()
		c.Assert(p.GetStatus(), qt.Equals, models.ProcessStatus_READY)
		c.Assert(p.GetMode().GetInterruptible(), qt.IsFalse) // caller's flag honoured
	})

	t.Run("explicit READY honours caller Interruptible", func(t *testing.T) {
		c := qt.New(t)
		tx, err := a.BuildNewProcessTx(newParams(models.ProcessStatus_READY, false))
		c.Assert(err, qt.IsNil)
		p := tx.GetNewProcess().GetProcess()
		c.Assert(p.GetStatus(), qt.Equals, models.ProcessStatus_READY)
		c.Assert(p.GetMode().GetInterruptible(), qt.IsFalse)
	})

	t.Run("explicit PAUSED forces Interruptible on", func(t *testing.T) {
		c := qt.New(t)
		tx, err := a.BuildNewProcessTx(newParams(models.ProcessStatus_PAUSED, false))
		c.Assert(err, qt.IsNil)
		p := tx.GetNewProcess().GetProcess()
		c.Assert(p.GetStatus(), qt.Equals, models.ProcessStatus_PAUSED)
		// a paused election that is not interruptible can never be resumed on-chain (the
		// vochain refuses SET_PROCESS_STATUS on it), so the builder must override the caller.
		c.Assert(p.GetMode().GetInterruptible(), qt.IsTrue)
	})

	t.Run("PAUSED keeps Interruptible when caller already asked for it", func(t *testing.T) {
		c := qt.New(t)
		tx, err := a.BuildNewProcessTx(newParams(models.ProcessStatus_PAUSED, true))
		c.Assert(err, qt.IsNil)
		p := tx.GetNewProcess().GetProcess()
		c.Assert(p.GetStatus(), qt.Equals, models.ProcessStatus_PAUSED)
		c.Assert(p.GetMode().GetInterruptible(), qt.IsTrue)
	})

	t.Run("status outside READY/PAUSED whitelist is rejected", func(t *testing.T) {
		c := qt.New(t)
		for _, bad := range []models.ProcessStatus{
			models.ProcessStatus_ENDED,
			models.ProcessStatus_CANCELED,
			models.ProcessStatus_RESULTS,
		} {
			_, err := a.BuildNewProcessTx(newParams(bad, false))
			c.Assert(err, qt.ErrorMatches, `initialStatus must be READY or PAUSED, got .*`,
				qt.Commentf("status=%s", bad))
		}
	})
}
