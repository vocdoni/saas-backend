package api

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
	"golang.org/x/sync/singleflight"
)

const (
	// electionReadCacheSize bounds each public election read cache (same order of magnitude as the
	// legacy election cache).
	electionReadCacheSize = 4096
	// liveElectionReadTTL is how long a not-yet-final election read is served from cache before a
	// public read fetches it from the chain again. Short, so live tallies stay fresh while a burst
	// of polls collapses into one chain round-trip per election per TTL.
	liveElectionReadTTL = 5 * time.Second
	// finishedElectionReadTTL is the cache TTL for elections whose results can no longer change
	// (final results, RESULTS or CANCELED status): they only need an occasional refresh.
	finishedElectionReadTTL = 5 * time.Minute
	// maxConcurrentElectionReads bounds the service-wide number of in-flight chain reads issued by
	// the public read paths, so request bursts queue instead of swamping the Vochain node.
	maxConcurrentElectionReads = 32
)

// electionReads coalesces and caches the election fetches of the public read paths
// (GET /processes/{id}, /questions/{qid} and /results). Concurrent requests for the same election
// share one chain round-trip (singleflight), results are cached with a short TTL (longer once the
// election is finished), and a service-wide slot pool bounds the number of concurrent chain reads.
type electionReads struct {
	fetch    func(internal.HexBytes) (*dvoteapi.Election, error)
	group    singleflight.Group
	live     *expirable.LRU[string, *dvoteapi.Election]
	finished *expirable.LRU[string, *dvoteapi.Election]
	slots    chan struct{}
}

// newElectionReads builds an electionReads around the given chain fetch function.
func newElectionReads(fetch func(internal.HexBytes) (*dvoteapi.Election, error)) *electionReads {
	return &electionReads{
		fetch:    fetch,
		live:     expirable.NewLRU[string, *dvoteapi.Election](electionReadCacheSize, nil, liveElectionReadTTL),
		finished: expirable.NewLRU[string, *dvoteapi.Election](electionReadCacheSize, nil, finishedElectionReadTTL),
		slots:    make(chan struct{}, maxConcurrentElectionReads),
	}
}

// electionIsFinished reports whether an election's results can no longer change, so its read can be
// cached for longer. ENDED is deliberately not final: it still moves to RESULTS.
func electionIsFinished(e *dvoteapi.Election) bool {
	return e.FinalResults || e.Status == "RESULTS" || e.Status == "CANCELED"
}

// cached returns the cached election for key, preferring the finished cache.
func (er *electionReads) cached(key string) (*dvoteapi.Election, bool) {
	if e, ok := er.finished.Get(key); ok {
		return e, true
	}
	if e, ok := er.live.Get(key); ok {
		return e, true
	}
	return nil, false
}

// election returns the election identified by id, serving it from cache when fresh and otherwise
// fetching it from the chain, coalescing concurrent fetches of the same election into one. ctx only
// bounds this caller's wait: a cancelled request stops waiting (and stops launching further
// fetches), while an in-flight shared fetch completes and populates the cache for the next reader.
func (er *electionReads) election(ctx context.Context, id internal.HexBytes) (*dvoteapi.Election, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := id.String()
	if e, ok := er.cached(key); ok {
		return e, nil
	}
	ch := er.group.DoChan(key, func() (any, error) {
		// double-check: a just-finished flight may have populated the cache
		if e, ok := er.cached(key); ok {
			return e, nil
		}
		er.slots <- struct{}{}
		defer func() { <-er.slots }()
		e, err := er.fetch(id)
		if err != nil {
			return nil, err
		}
		if electionIsFinished(e) {
			er.finished.Add(key, e)
		} else {
			er.live.Add(key, e)
		}
		return e, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		e, ok := res.Val.(*dvoteapi.Election)
		if !ok {
			return nil, fmt.Errorf("unexpected election read result type %T", res.Val)
		}
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// acquireSlot takes a service-wide chain-read slot for a non-election chain fetch (e.g. encryption
// keys), abandoning the wait when ctx is cancelled. Returns whether the slot was acquired; the
// caller must releaseSlot when it was.
func (er *electionReads) acquireSlot(ctx context.Context) bool {
	select {
	case er.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseSlot returns a slot taken by acquireSlot.
func (er *electionReads) releaseSlot() { <-er.slots }
