package db

import (
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

// TestMeterRelayedVote pins that a vote is metered once per nullifier, however many relays of it
// (re-relays, overwrites, concurrent relays) confirm it on chain.
func TestMeterRelayedVote(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	sentVotes := func() int {
		org, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		return org.Counters.SentVotes
	}

	_, err := testDB.MeterRelayedVote(testOrgAddress, nil)
	c.Assert(err, qt.ErrorIs, ErrBadInputs)

	first := internal.HexBytes{0x01, 0x02}
	metered, err := testDB.MeterRelayedVote(testOrgAddress, first)
	c.Assert(err, qt.IsNil)
	c.Assert(metered, qt.IsTrue)
	// a re-relay or an overwrite of the same vote is not metered again
	metered, err = testDB.MeterRelayedVote(testOrgAddress, first)
	c.Assert(err, qt.IsNil)
	c.Assert(metered, qt.IsFalse)
	c.Assert(sentVotes(), qt.Equals, 1)

	// relays of one vote racing each other meter it once
	second := internal.HexBytes{0x03, 0x04}
	var wg sync.WaitGroup
	results := make([]bool, 8)
	for i := range results {
		wg.Go(func() {
			ok, err := testDB.MeterRelayedVote(testOrgAddress, second)
			c.Check(err, qt.IsNil)
			results[i] = ok
		})
	}
	wg.Wait()
	meteredCount := 0
	for _, ok := range results {
		if ok {
			meteredCount++
		}
	}
	c.Assert(meteredCount, qt.Equals, 1)
	c.Assert(sentVotes(), qt.Equals, 2)
}
