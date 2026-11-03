package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Request admission. Every route takes one of maxInFlightRequests slots for as long as its handler
// runs. A request that finds them all taken waits in a backlog for up to requestBacklogTimeout
// instead of getting a 429 at once, so a short burst costs milliseconds of latency, not errors.
//
// The routes that may wait on the chain (public process, question and results reads, vote
// verification) first take one of maxInFlightChainRequests slots, and only then a general one. A
// slow chain therefore queues those requests in their own backlog, where they hold no general
// slot, and can never take more than maxInFlightChainRequests of the general slots: CSP auth and
// signing, vote relay and job polling, which never read the chain, keep the rest.
const (
	// maxInFlightRequests bounds the requests running handlers at once. Raising it does not add
	// capacity: cheap handlers saturate the CPU long before this many are in flight, and slow
	// ones would only hold more memory and goroutines.
	maxInFlightRequests = 100
	// requestBacklog bounds the requests waiting for a general slot; beyond it they get a 429.
	requestBacklog = 1000
	// requestBacklogTimeout is how long a request waits for a general slot before a 429.
	requestBacklogTimeout = 5 * time.Second

	// maxInFlightChainRequests bounds the chain-bound routes running at once. It stays above
	// maxConcurrentElectionReads so cached reads keep being served while every chain read slot
	// is busy, and well below maxInFlightRequests so those routes cannot starve the others.
	maxInFlightChainRequests = 50
	// chainRequestBacklog bounds the chain-bound requests waiting for a slot.
	chainRequestBacklog = 1000
	// chainRequestBacklogTimeout is how long a chain-bound request waits for a slot. Longer than
	// requestBacklogTimeout: a slow chain is expected to make these wait.
	chainRequestBacklogTimeout = 10 * time.Second

	// throttleRetryAfter is the Retry-After sent with a throttling 429.
	throttleRetryAfter = time.Second
)

// newRequestThrottle returns the admission middleware every route goes through. All the route
// groups must share the one instance it returns, since each instance has its own slots.
func newRequestThrottle() func(http.Handler) http.Handler {
	return middleware.ThrottleWithOpts(middleware.ThrottleOpts{
		Limit:          maxInFlightRequests,
		BacklogLimit:   requestBacklog,
		BacklogTimeout: requestBacklogTimeout,
		RetryAfterFn:   throttleRetryAfterFn,
	})
}

// newChainRequestThrottle returns the admission middleware of the chain-bound routes, to be used
// before the one of newRequestThrottle.
func newChainRequestThrottle() func(http.Handler) http.Handler {
	return middleware.ThrottleWithOpts(middleware.ThrottleOpts{
		Limit:          maxInFlightChainRequests,
		BacklogLimit:   chainRequestBacklog,
		BacklogTimeout: chainRequestBacklogTimeout,
		RetryAfterFn:   throttleRetryAfterFn,
	})
}

func throttleRetryAfterFn(bool) time.Duration { return throttleRetryAfter }
