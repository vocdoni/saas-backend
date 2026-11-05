package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// serve runs one request through h and returns its status code.
func serve(h http.Handler) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	return rec.Code
}

// TestRequestThrottleQueuesBursts asserts a burst above maxInFlightRequests waits for a slot
// instead of getting a 429.
func TestRequestThrottleQueuesBursts(t *testing.T) {
	c := qt.New(t)
	var inFlight, peak atomic.Int64
	h := newRequestThrottle()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))

	const requests = 2 * maxInFlightRequests
	codes := make([]int, requests)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Go(func() { codes[i] = serve(h) })
	}
	wg.Wait()
	for i, code := range codes {
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("request %d", i))
	}
	c.Assert(peak.Load() <= maxInFlightRequests, qt.IsTrue, qt.Commentf("peak %d in flight", peak.Load()))
}

// TestChainRequestThrottleKeepsSlotsForOtherRoutes wires the throttles as initRouter does and
// asserts that chain-bound routes stuck on a slow chain, with more of them queued, leave general
// slots to the routes that do not read the chain.
func TestChainRequestThrottleKeepsSlotsForOtherRoutes(t *testing.T) {
	c := qt.New(t)
	throttle := newRequestThrottle()
	release := make(chan struct{})
	var chainInFlight atomic.Int64
	entered := make(chan struct{}, maxInFlightChainRequests)
	chainRoute := newChainRequestThrottle(throttle)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chainInFlight.Add(1)
		entered <- struct{}{}
		<-release
		chainInFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	otherRoute := throttle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const chainRequests = maxInFlightChainRequests + 20
	codes := make([]int, chainRequests)
	var wg sync.WaitGroup
	for i := range chainRequests {
		wg.Go(func() { codes[i] = serve(chainRoute) })
	}
	for range maxInFlightChainRequests {
		<-entered
	}

	// the chain-bound routes hold all their slots and have a queue, yet another route is served
	start := time.Now()
	c.Assert(serve(otherRoute), qt.Equals, http.StatusOK)
	c.Assert(time.Since(start) < time.Second, qt.IsTrue, qt.Commentf("took %s", time.Since(start)))
	c.Assert(chainInFlight.Load(), qt.Equals, int64(maxInFlightChainRequests))

	// once the chain answers, the queued chain-bound requests are served too
	close(release)
	wg.Wait()
	for i, code := range codes {
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("request %d", i))
	}
}
