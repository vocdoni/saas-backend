package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Request admission. Every route but the /ping health check takes one of maxInFlightRequests slots
// for as long as its handler runs. A request that finds them all taken waits in a backlog for up to
// requestBacklogTimeout instead of getting a 429 at once, so a short burst costs milliseconds of
// latency, not errors.
//
// The public routes that may wait on the chain (process, question and results reads, vote
// verification and the CSP check, which reads the chain for hasVoted) first take one of
// maxInFlightChainRequests slots, and only then a general one. A slow chain therefore queues those
// requests in their own backlog, where they hold no general slot, and can never take more than
// maxInFlightChainRequests of the general slots: CSP auth and signing, vote relay and job polling,
// which never read the chain, keep the rest.
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
	// chainRequestBacklog bounds the chain-bound requests waiting for a slot. With a chain answering
	// in 1 s, the slots drain about this many in chainRequestBacklogTimeout; a deeper backlog would
	// only make the extra requests wait the whole timeout for a 429.
	chainRequestBacklog = 500
	// chainRequestBacklogTimeout is how long a chain-bound request waits for a slot. Longer than
	// requestBacklogTimeout: a slow chain is expected to make these wait.
	chainRequestBacklogTimeout = 10 * time.Second
)

// newRequestThrottle returns the admission middleware every throttled route goes through. All the
// route groups must share the one instance it returns, since each instance has its own slots.
func newRequestThrottle() func(http.Handler) http.Handler {
	return throttle(maxInFlightRequests, requestBacklog, requestBacklogTimeout)
}

// newChainRequestThrottle returns the admission middleware of the chain-bound routes: a slot of
// their own first, then one of general, the shared instance from newRequestThrottle.
func newChainRequestThrottle(general func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	chain := throttle(maxInFlightChainRequests, chainRequestBacklog, chainRequestBacklogTimeout)
	return func(next http.Handler) http.Handler { return chain(general(next)) }
}

// throttle admits limit requests at once and queues backlog more for up to timeout. A rejected
// request is told to retry after that same timeout, so clients turned away together do not all
// come back at once.
func throttle(limit, backlog int, timeout time.Duration) func(http.Handler) http.Handler {
	return middleware.ThrottleWithOpts(middleware.ThrottleOpts{
		Limit:          limit,
		BacklogLimit:   backlog,
		BacklogTimeout: timeout,
		RetryAfterFn:   func(bool) time.Duration { return timeout },
	})
}
