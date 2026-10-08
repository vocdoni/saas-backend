package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
)

// TestElectionReadsCoalescing checks that concurrent reads of the same election share one chain
// fetch and that the result is then served from cache.
func TestElectionReadsCoalescing(t *testing.T) {
	c := qt.New(t)
	var fetches atomic.Int64
	release := make(chan struct{})
	er := newElectionReads(func(_ internal.HexBytes) (*dvoteapi.Election, error) {
		fetches.Add(1)
		<-release
		return &dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "READY"}}, nil
	})

	id := internal.HexBytes{0x01, 0x02}
	const callers = 20
	var wg sync.WaitGroup
	results := make([]*dvoteapi.Election, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Go(func() {
			results[i], errs[i] = er.election(context.Background(), id)
		})
	}
	// let every caller reach the shared flight before releasing the fetch
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	c.Assert(fetches.Load(), qt.Equals, int64(1))
	for i := range callers {
		c.Assert(errs[i], qt.IsNil)
		c.Assert(results[i], qt.Not(qt.IsNil))
	}

	// a follow-up read within the live TTL is served from cache: no new fetch
	_, err := er.election(context.Background(), id)
	c.Assert(err, qt.IsNil)
	c.Assert(fetches.Load(), qt.Equals, int64(1))
}

// TestElectionReadsFinishedCache checks that a finished election is cached (so repeat reads do not
// hit the chain) and that the finished classification covers final results and terminal statuses.
func TestElectionReadsFinishedCache(t *testing.T) {
	c := qt.New(t)
	var fetches atomic.Int64
	er := newElectionReads(func(_ internal.HexBytes) (*dvoteapi.Election, error) {
		fetches.Add(1)
		return &dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "RESULTS", FinalResults: true}}, nil
	})

	id := internal.HexBytes{0xaa}
	for range 5 {
		e, err := er.election(context.Background(), id)
		c.Assert(err, qt.IsNil)
		c.Assert(e.FinalResults, qt.IsTrue)
	}
	c.Assert(fetches.Load(), qt.Equals, int64(1))

	// classification: ENDED is not final (it still moves to RESULTS), RESULTS/CANCELED are
	c.Assert(electionIsFinished(&dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "ENDED"}}), qt.IsFalse)
	c.Assert(electionIsFinished(&dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "READY"}}), qt.IsFalse)
	c.Assert(electionIsFinished(&dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "RESULTS"}}), qt.IsTrue)
	c.Assert(electionIsFinished(&dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "CANCELED"}}), qt.IsTrue)
	c.Assert(electionIsFinished(
		&dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "READY", FinalResults: true}}), qt.IsTrue)
}

// TestElectionReadsContextCancel checks that a cancelled request stops waiting for the shared fetch
// and that a pre-cancelled context never launches one.
func TestElectionReadsContextCancel(t *testing.T) {
	c := qt.New(t)
	var fetches atomic.Int64
	release := make(chan struct{})
	er := newElectionReads(func(_ internal.HexBytes) (*dvoteapi.Election, error) {
		fetches.Add(1)
		<-release
		return &dvoteapi.Election{ElectionSummary: dvoteapi.ElectionSummary{Status: "READY"}}, nil
	})
	defer close(release)

	// pre-cancelled context: no fetch at all
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := er.election(cancelled, internal.HexBytes{0x0f})
	c.Assert(err, qt.ErrorIs, context.Canceled)
	c.Assert(fetches.Load(), qt.Equals, int64(0))

	// a caller whose context is cancelled mid-flight returns promptly with ctx.Err()
	ctx, cancelMid := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := er.election(ctx, internal.HexBytes{0x0f})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // let the flight start
	cancelMid()
	select {
	case err := <-done:
		c.Assert(err, qt.ErrorIs, context.Canceled)
	case <-time.After(2 * time.Second):
		c.Fatal("cancelled election read did not return")
	}
	c.Assert(fetches.Load(), qt.Equals, int64(1))
}
