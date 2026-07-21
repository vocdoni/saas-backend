// Package statussync provides an enqueue-driven worker that reconciles a published voting-process
// question's stored on-chain status with the Vochain on demand, rather than sweeping every question
// on a timer. Work is fed by two triggers: a status change made through the API (confirm the tx
// landed) and a read of a process/question (catch changes made directly on-chain). The one
// safety-critical reader — the managed-org delete guard — reads the chain synchronously instead of
// trusting the stored status, so unread transitions never block or allow a deletion wrongly.
package statussync

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultInterval is the poll cadence between confirm retries and the read-path freshness
	// window, when Config.Interval is 0.
	defaultInterval = 60 * time.Second
	// defaultConfirmTimeout bounds how long a status-change is polled for on-chain confirmation
	// before giving up and reconciling to whatever the chain reports, when Config.ConfirmTimeout is 0.
	defaultConfirmTimeout = 5 * time.Minute
	// defaultWorkers caps concurrent chain reads per processing pass.
	defaultWorkers = 8
)

// Config wires the syncer's dependencies and tuning. Zero Interval/ConfirmTimeout/Workers default.
type Config struct {
	DB             *db.MongoStorage
	Account        *account.Account
	Interval       time.Duration
	ConfirmTimeout time.Duration
	Workers        int
}

// task is one queued reconciliation, keyed (for dedup) by its on-chain election id.
//
//   - expected == "" is a one-shot reconcile (read path): converge the stored status to whatever
//     the chain reports, then drop.
//   - expected != "" is a confirm (status-change path): poll until the chain reaches expected, then
//     drop; while it hasn't, do not write (the stored status is already optimistically expected —
//     writing the still-old chain value would flap it). On the last attempt, reconcile to the actual
//     chain status so a failed/never-mined tx self-corrects.
type task struct {
	upstreamID internal.HexBytes
	known      string    // stored status at enqueue — guards the reconcile write against concurrent writes
	expected   string    // "" = reconcile; else the confirm target
	attempts   int       // remaining chain reads (reconcile = 1)
	nextAt     time.Time // earliest time to process (confirm retries back off by interval)
}

// Syncer reconciles question statuses with the chain from an in-memory queue. A single loop
// goroutine processes due tasks through a bounded worker pool; ctx cancellation stops it.
type Syncer struct {
	db          *db.MongoStorage
	account     *account.Account
	interval    time.Duration
	maxAttempts int
	workers     int
	ctx         context.Context

	mu      sync.Mutex
	pending map[string]*task
	wake    chan struct{}
}

// New builds a Syncer bound to ctx (cancelling ctx stops it). Zero tuning fields use the defaults.
func New(ctx context.Context, c *Config) *Syncer {
	interval := c.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	confirmTimeout := c.ConfirmTimeout
	if confirmTimeout <= 0 {
		confirmTimeout = defaultConfirmTimeout
	}
	workers := c.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	maxAttempts := int(confirmTimeout / interval)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &Syncer{
		db:          c.DB,
		account:     c.Account,
		interval:    interval,
		maxAttempts: maxAttempts,
		workers:     workers,
		ctx:         ctx,
		pending:     make(map[string]*task),
		wake:        make(chan struct{}, 1),
	}
}

// Start launches the processing loop and returns immediately. The loop wakes on each enqueue and at
// least every interval to service confirm retries. ctx cancellation stops it.
func (s *Syncer) Start() {
	log.Infow("starting question status syncer",
		"interval", s.interval.String(), "maxAttempts", s.maxAttempts, "workers", s.workers)
	go s.loop()
}

func (s *Syncer) loop() {
	for {
		s.processDue(false)
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-time.After(s.interval):
		}
	}
}

// EnqueueConfirm queues a status-change confirmation: poll the chain until the question reaches
// expected (up to maxAttempts, one per interval), then stop. First poll is one interval out to give
// the tx time to mine. A no-op on a nil syncer or empty id.
func (s *Syncer) EnqueueConfirm(upstreamID internal.HexBytes, expected string) {
	if s == nil || len(upstreamID) == 0 {
		return
	}
	s.add(&task{
		upstreamID: upstreamID,
		expected:   expected,
		attempts:   s.maxAttempts,
		nextAt:     time.Now().Add(s.interval),
	})
}

// EnqueueReconcile queues a one-shot reconcile triggered by a read: known is the stored status the
// caller just served, used to guard the conditional write. Processed on the next loop pass. A no-op
// on a nil syncer or empty id.
func (s *Syncer) EnqueueReconcile(upstreamID internal.HexBytes, known string) {
	if s == nil || len(upstreamID) == 0 {
		return
	}
	s.add(&task{
		upstreamID: upstreamID,
		known:      known,
		attempts:   1,
		nextAt:     time.Now(),
	})
}

// add inserts (deduped by upstreamID) and signals the loop. A pending confirm is never downgraded
// to a reconcile; otherwise the newer task replaces the older one.
func (s *Syncer) add(t *task) {
	key := hex.EncodeToString(t.upstreamID)
	s.mu.Lock()
	if existing, ok := s.pending[key]; ok && existing.expected != "" && t.expected == "" {
		s.mu.Unlock()
		return
	}
	s.pending[key] = t
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ProcessPending processes every pending task once, ignoring nextAt scheduling, and returns how
// many it handled. Exported as a deterministic test hook; production uses the interval loop.
func (s *Syncer) ProcessPending() int {
	return s.processDue(true)
}

// processDue removes and processes tasks that are due (or all, when force), through a bounded worker
// pool. Confirm tasks that haven't converged reschedule themselves. Returns the count processed.
// force is an internal due-vs-all selector (the ProcessPending test hook vs the interval loop).
//
//nolint:revive // flag-parameter: force is an internal mode selector, not user-facing control coupling
func (s *Syncer) processDue(force bool) int {
	now := time.Now()
	s.mu.Lock()
	var due []*task
	for k, t := range s.pending {
		if force || !t.nextAt.After(now) {
			due = append(due, t)
			delete(s.pending, k)
		}
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return 0
	}
	sem := make(chan struct{}, s.workers)
	var wg sync.WaitGroup
	for _, t := range due {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			s.process(t)
		})
	}
	wg.Wait()
	return len(due)
}

// process performs one chain read for a task and applies the outcome (see task's doc for the
// confirm-vs-reconcile rules).
func (s *Syncer) process(t *task) {
	hexID := hex.EncodeToString(t.upstreamID)
	election, err := s.account.Election(t.upstreamID)
	if err != nil {
		log.Warnw("status sync: chain read failed", "upstreamId", hexID, "error", err.Error())
		s.retryOrDrop(t)
		return
	}
	chainStatus := election.Status

	// read path: converge stored status to the chain (also refreshes syncedAt), then drop.
	if t.expected == "" {
		if _, err := s.db.SetQuestionStatusSynced(t.upstreamID, t.known, chainStatus); err != nil {
			log.Warnw("status sync: reconcile write failed", "upstreamId", hexID, "error", err.Error())
		}
		return
	}

	// status-change path: confirmed once the chain reaches expected — just refresh syncedAt.
	if chainStatus == t.expected {
		if _, err := s.db.SetQuestionStatusSynced(t.upstreamID, t.expected, t.expected); err != nil {
			log.Warnw("status sync: confirm stamp failed", "upstreamId", hexID, "error", err.Error())
		}
		return
	}
	// not confirmed yet: keep polling without touching the optimistic stored value.
	if t.attempts > 1 {
		t.attempts--
		t.nextAt = time.Now().Add(s.interval)
		s.add(t)
		return
	}
	// gave up: reconcile the optimistic value to whatever the chain actually holds.
	if _, err := s.db.SetQuestionStatusSynced(t.upstreamID, t.expected, chainStatus); err != nil {
		log.Warnw("status sync: give-up reconcile failed", "upstreamId", hexID, "error", err.Error())
	}
}

// retryOrDrop reschedules a confirm task with attempts left after a transient chain-read error;
// reconcile tasks (and exhausted confirms) are dropped and left for the next read to re-enqueue.
func (s *Syncer) retryOrDrop(t *task) {
	if t.expected != "" && t.attempts > 1 {
		t.attempts--
		t.nextAt = time.Now().Add(s.interval)
		s.add(t)
	}
}
