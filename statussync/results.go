package statussync

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	vocdoniapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultResultsWorkers caps the elections processed concurrently by the results queue, when
	// Config.ResultsWorkers is 0.
	defaultResultsWorkers = 2
	// awaitFinalInterval is the cadence at which an awaitFinal task re-reads an ENDED election, waiting
	// for its final results, and the results loop's idle wake-up.
	awaitFinalInterval = 30 * time.Second
	// awaitFinalAttempts bounds the election reads of an awaitFinal task before it is dropped.
	awaitFinalAttempts = 60
	// resultsSuccessHold is how long an id whose processors all succeeded ignores new enqueues.
	resultsSuccessHold = 5 * time.Minute
	// resultsMinFailureHold and resultsMaxFailureHold bound the exponential hold after a failed run:
	// 1, 2, 4 ... minutes, capped, and reset by a successful run.
	resultsMinFailureHold = time.Minute
	resultsMaxFailureHold = time.Hour
	// resultsHoldsSize bounds the in-memory holds; their TTL outlives the longest hold, so a failure
	// count survives until the next attempt.
	resultsHoldsSize = 4096
	resultsHoldsTTL  = 2 * resultsMaxFailureHold
)

// ResultsProcessor derives and persists extra data from a question's final on-chain results.
// Process is invoked once the question's election reports FinalResults; it must be idempotent and
// cheap (no chain calls) when its data is already stored, because it can be invoked on every trigger.
type ResultsProcessor interface {
	Name() string
	Process(ctx context.Context, store *db.MongoStorage, q *db.VotingProcessQuestion, e *vocdoniapi.Election) error
}

// resultsTask is one queued run of the results processors for a question, keyed by its on-chain id.
// A plain task runs as soon as it is due; an awaitFinal task re-reads the election every
// awaitFinalInterval until it reports final results (then runs the processors) or attempts run out.
type resultsTask struct {
	upstreamID internal.HexBytes
	awaitFinal bool
	attempts   int       // awaitFinal: remaining election reads
	nextAt     time.Time // earliest time to run
}

// resultsHold throttles an id after a run: enqueues before until are dropped. failures counts the
// consecutive failed runs and sizes the next failure hold.
type resultsHold struct {
	until    time.Time
	failures int
}

// resultsOutcome is how a results run ended, which decides the id's hold.
type resultsOutcome int

const (
	resultsNotFinal  resultsOutcome = iota // the election has no final results yet: no hold
	resultsSucceeded                       // every processor succeeded: success hold
	resultsFailed                          // a read or a processor failed: exponential failure hold
)

// resultsQueue is the results processors' own queue, separate from the status queue so a long
// processor run never stalls status sync. Dedup is by id across pending and in-flight tasks.
type resultsQueue struct {
	processors []ResultsProcessor
	workers    int
	// question and election are the worker's reads, bound by New to the DB and the chain account;
	// they are fields only so unit tests can run the queue without Mongo or a node.
	question func(internal.HexBytes) (*db.VotingProcessQuestion, error)
	election func([]byte) (*vocdoniapi.Election, error)

	mu       sync.Mutex
	pending  map[string]*resultsTask
	inFlight map[string]bool
	holds    *expirable.LRU[string, resultsHold]
	wake     chan struct{}
}

func newResultsQueue(c *Config) *resultsQueue {
	workers := c.ResultsWorkers
	if workers <= 0 {
		workers = defaultResultsWorkers
	}
	return &resultsQueue{
		processors: c.ResultsProcessors,
		workers:    workers,
		question:   c.DB.QuestionByUpstreamID,
		election:   c.Account.Election,
		pending:    make(map[string]*resultsTask),
		inFlight:   make(map[string]bool),
		holds:      expirable.NewLRU[string, resultsHold](resultsHoldsSize, nil, resultsHoldsTTL),
		wake:       make(chan struct{}, 1),
	}
}

// EnqueueResults queues a run of the results processors for a question whose election reports final
// results. A no-op on a nil syncer, an empty id, or when no processors are configured. Dropped when
// the id is already pending or running, or held after a recent run; it replaces a pending awaitFinal
// task, since finality is now known.
func (s *Syncer) EnqueueResults(upstreamID internal.HexBytes) {
	if s == nil || len(upstreamID) == 0 || len(s.results.processors) == 0 {
		return
	}
	s.results.add(&resultsTask{upstreamID: upstreamID, nextAt: time.Now()})
}

// enqueueAwaitFinal queues a follow-up for an ENDED election that has no final results yet: it polls
// the election until they are published, then runs the processors. It never replaces a pending task.
func (s *Syncer) enqueueAwaitFinal(upstreamID internal.HexBytes) {
	if len(upstreamID) == 0 || len(s.results.processors) == 0 {
		return
	}
	s.results.add(&resultsTask{
		upstreamID: upstreamID,
		awaitFinal: true,
		attempts:   awaitFinalAttempts,
		nextAt:     time.Now().Add(awaitFinalInterval),
	})
}

// followResults is the status pass's results trigger for a task that will not read the chain again:
// a final election queues the processors, an ENDED one that is not final yet an awaitFinal follow-up.
func (s *Syncer) followResults(upstreamID internal.HexBytes, election *vocdoniapi.Election) {
	switch {
	case election.FinalResults:
		s.EnqueueResults(upstreamID)
	case election.Status == db.QuestionStatusEnded:
		s.enqueueAwaitFinal(upstreamID)
	default:
	}
}

func (rq *resultsQueue) add(t *resultsTask) {
	key := hex.EncodeToString(t.upstreamID)
	rq.mu.Lock()
	if h, ok := rq.holds.Peek(key); ok && time.Now().Before(h.until) {
		rq.mu.Unlock()
		return
	}
	// an awaitFinal never replaces a pending task, and a plain task replaces only a pending awaitFinal
	if existing, ok := rq.pending[key]; rq.inFlight[key] || ok && (t.awaitFinal || !existing.awaitFinal) {
		rq.mu.Unlock()
		return
	}
	rq.pending[key] = t
	rq.mu.Unlock()
	select {
	case rq.wake <- struct{}{}:
	default:
	}
}

// takeDue moves the due tasks (or all of them, when force) from pending to in-flight.
//
//nolint:revive // flag-parameter: force is an internal mode selector (drainResults vs the loop), as in processDue
func (rq *resultsQueue) takeDue(force bool) []*resultsTask {
	now := time.Now()
	rq.mu.Lock()
	defer rq.mu.Unlock()
	var due []*resultsTask
	for k, t := range rq.pending {
		if force || !t.nextAt.After(now) {
			due = append(due, t)
			delete(rq.pending, k)
			rq.inFlight[k] = true
		}
	}
	return due
}

// finish releases an in-flight task and applies its outcome: a hold for a completed run, a reschedule
// for an awaitFinal task whose election is not final yet (dropped once its attempts run out).
func (rq *resultsQueue) finish(t *resultsTask, outcome resultsOutcome) {
	key := hex.EncodeToString(t.upstreamID)
	now := time.Now()
	rq.mu.Lock()
	defer rq.mu.Unlock()
	delete(rq.inFlight, key)
	switch outcome {
	case resultsSucceeded:
		rq.holds.Add(key, resultsHold{until: now.Add(resultsSuccessHold)})
	case resultsFailed:
		h, _ := rq.holds.Peek(key)
		h.failures++
		h.until = now.Add(failureHold(h.failures))
		rq.holds.Add(key, h)
		log.Warnw("results processing failed", "upstreamId", key, "attempt", h.failures, "nextEligible", h.until)
	default:
		if t.awaitFinal && t.attempts > 1 {
			t.attempts--
			t.nextAt = now.Add(awaitFinalInterval)
			rq.pending[key] = t
		}
	}
}

// failureHold is the hold after the given number of consecutive failed runs: 1 min doubling per
// failure, capped at resultsMaxFailureHold.
func failureHold(failures int) time.Duration {
	hold := resultsMinFailureHold
	for range failures - 1 {
		hold *= 2
		if hold >= resultsMaxFailureHold {
			return resultsMaxFailureHold
		}
	}
	return hold
}

// resultsLoop runs due results tasks on at most results.workers goroutines, without waiting for a
// batch to finish before taking the next due task. ctx cancellation stops it.
func (s *Syncer) resultsLoop() {
	slots := make(chan struct{}, s.results.workers)
	for {
		for _, t := range s.results.takeDue(false) {
			select {
			case slots <- struct{}{}:
			case <-s.ctx.Done():
				return
			}
			go func() {
				defer func() { <-slots }()
				s.runResults(t)
			}()
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.results.wake:
		case <-time.After(awaitFinalInterval):
		}
	}
}

// drainResults runs every pending results task once, synchronously, ignoring nextAt. An awaitFinal
// task whose election is still not final stays pending with one attempt less (so a test can drain
// again), or is dropped when exhausted.
func (s *Syncer) drainResults() {
	for _, t := range s.results.takeDue(true) {
		s.runResults(t)
	}
}

// runResults loads the question and its election and, once the election is final, runs every
// processor in order. A failing processor is logged and does not stop the others; nothing is retried
// immediately, the failure hold decides when a later trigger may run the id again.
func (s *Syncer) runResults(t *resultsTask) {
	s.results.finish(t, s.processResults(t))
}

func (s *Syncer) processResults(t *resultsTask) resultsOutcome {
	hexID := hex.EncodeToString(t.upstreamID)
	q, err := s.results.question(t.upstreamID)
	if err != nil {
		log.Warnw("results: question read failed", "upstreamId", hexID, "error", err.Error())
		return readFailure(t)
	}
	election, err := s.results.election(t.upstreamID)
	if err != nil {
		log.Warnw("results: chain read failed", "upstreamId", hexID, "error", err.Error())
		return readFailure(t)
	}
	if !election.FinalResults {
		return resultsNotFinal
	}
	outcome := resultsSucceeded
	for _, p := range s.results.processors {
		if err := p.Process(s.ctx, s.db, q, election); err != nil {
			log.Warnw("results processor failed", "processor", p.Name(), "upstreamId", hexID, "error", err.Error())
			outcome = resultsFailed
		}
	}
	return outcome
}

// readFailure is the outcome of a run that could not read its question or election: a failed run for a
// plain task (failure hold), but for an awaitFinal task just one more poll that found nothing, so a
// transient blip does not end the follow-up — it keeps polling with one attempt less.
func readFailure(t *resultsTask) resultsOutcome {
	if t.awaitFinal {
		return resultsNotFinal
	}
	return resultsFailed
}
