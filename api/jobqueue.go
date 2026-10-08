package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// orgTxLock is a held per-organization lock. The holder (usually a queue worker that was
// handed the lock across the async hand-off) releases it with Unlock after the on-chain
// submit completes.
type orgTxLock struct {
	ch chan struct{}
}

// Unlock releases the lock so the next waiter can acquire it.
func (l *orgTxLock) Unlock() { <-l.ch }

// orgTxMutex hands out a per-organization lock so the build->sign->submit pipeline for
// backend-submitted txs (publish and status change) is serialized per org. Two concurrent
// such requests for the same org would otherwise read the same account nonce and sign
// conflicting transactions. In-process only — a multi-instance deployment would
// need a distributed lock, matching the single-instance assumption of db.keysLock. The
// locks map grows unbounded; an org count high enough to matter is not realistic here.
//
// The lock is a one-slot channel rather than a sync.Mutex so acquisition can give up: a
// worker holds it through tx mining (minutes), and a request blocked on a plain mutex
// for that long would hold a router slot with no way to answer the caller.
type orgTxMutex struct {
	mu    sync.Mutex
	locks map[common.Address]chan struct{}
}

func newOrgTxMutex() *orgTxMutex {
	return &orgTxMutex{locks: make(map[common.Address]chan struct{})}
}

// channel returns the one-slot channel backing addr's lock, creating it on first use.
func (o *orgTxMutex) channel(addr common.Address) chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	ch, ok := o.locks[addr]
	if !ok {
		ch = make(chan struct{}, 1)
		o.locks[addr] = ch
	}
	return ch
}

// lock acquires and returns the lock for addr, blocking for as long as it takes. Used
// where no HTTP caller is waiting on the acquisition (wallet debits, census growth).
func (o *orgTxMutex) lock(addr common.Address) *orgTxLock {
	ch := o.channel(addr)
	ch <- struct{}{}
	return &orgTxLock{ch: ch}
}

// orgTxLockTimeout bounds how long a request waits for its organization's tx lock before
// answering 503: a worker may legitimately hold the lock through tx mining, and a caller
// is better served by a clear "busy, retry later" than by a request parked for minutes.
// It is a var so tests can shorten it.
var orgTxLockTimeout = 10 * time.Second

// lockCtx acquires the lock for addr, waiting at most orgTxLockTimeout and no longer than
// the request context allows. It returns errors.ErrOrgTxBusy (503) when the organization
// already has a transaction in flight and the wait ran out — before any job is created,
// so a refused request leaves nothing behind.
func (o *orgTxMutex) lockCtx(ctx context.Context, addr common.Address) (*orgTxLock, error) {
	ch := o.channel(addr)
	select {
	case ch <- struct{}{}:
		return &orgTxLock{ch: ch}, nil
	default:
	}
	timer := time.NewTimer(orgTxLockTimeout)
	defer timer.Stop()
	select {
	case ch <- struct{}{}:
		return &orgTxLock{ch: ch}, nil
	case <-ctx.Done():
		return nil, errors.ErrOrgTxBusy.WithErr(ctx.Err())
	case <-timer.C:
		return nil, errors.ErrOrgTxBusy
	}
}

// pool sizes are consts; promote to config only if tuning is needed.
const (
	// relayTxQueueSize bounds the queued-but-not-yet-running vote relay tasks. It is sized to
	// hold several full vote batches at once: POST /votes reserves one slot per envelope
	// all-or-nothing, so a queue merely as large as one full batch (db.MaxQuestionsPerProcess)
	// would accept it only on a completely idle service, and a client turned away falls back to
	// relaying one vote at a time — the half-voted window that endpoint exists to close. The
	// buffer is not the bottleneck (the workers below are), so a bigger one adds no chain load
	// or concurrency; it only stops rejecting work the service can serve. A txTask is a string
	// and two func pointers, so this costs tens of kilobytes.
	relayTxQueueSize = 384
	// relayTxQueueWorkers caps concurrent vote submits so a chain stall cannot drain the
	// router's shared request budget or starve the public CSP voter path.
	relayTxQueueWorkers = 12

	// orgTxQueueSize bounds the queued organization-initiated tx tasks (publish, status and
	// census changes). These enqueue one task per request (a publish batches internally), so a
	// much smaller buffer than the relay queue already means dozens of organizations waiting.
	orgTxQueueSize = 128
	// orgTxQueueWorkers caps concurrent organization submits. Org tasks serialize per
	// organization on orgTxLocks anyway, so a small pool loses little parallelism — and
	// keeping it apart from the relay pool means a flood of public, unauthenticated
	// POST /votes traffic can never starve an organization's publish or status change
	// (and vice versa).
	orgTxQueueWorkers = 4
)

// txTask is a unit of background transaction work. run performs the on-chain submit
// plus any post-submit DB writes, returning the job result on success or an error on
// failure. The worker records the terminal outcome via db.SetJobStatus, unless record
// is set.
type txTask struct {
	jobID string
	run   func() (*db.JobResult, error)
	// record, when non-nil, takes over writing the outcome of this task. Tasks that
	// share a job with other tasks — the envelopes of one batch vote relay — need it:
	// the default path would mark the whole job terminal on the first outcome.
	record func(result *db.JobResult, err error)
}

// txQueue is one bounded task queue with all-or-nothing admission. The service runs two —
// one for organization-initiated txs, one for public vote relays — so that neither class
// of traffic can exhaust the other's slots or workers.
type txQueue struct {
	mu    sync.Mutex
	tasks chan txTask
}

// enqueueBatch hands a group of tasks to the queue all or nothing: it returns false,
// having enqueued none of them, when the queue cannot take the whole group. A caller
// relaying several votes at once therefore never leaves a voter half-voted because the
// queue filled up midway. The mutex makes the free-slot check and the sends atomic
// against each other; workers only ever drain the queue, so no concurrent producer can
// invalidate a check taken under the lock.
func (q *txQueue) enqueueBatch(tasks []txTask) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if cap(q.tasks)-len(q.tasks) < len(tasks) {
		return false
	}
	for _, task := range tasks {
		q.tasks <- task
	}
	return true
}

// startTxQueues creates the buffered queues and launches their worker pools. Called once
// from New(). No graceful drain — on process exit in-flight tasks die and their jobs stay
// `pending` until the next startup marks them failed (see db.FailInterruptedTxJobs).
func (a *API) startTxQueues() {
	a.orgTxQueue = &txQueue{tasks: make(chan txTask, orgTxQueueSize)}
	a.relayTxQueue = &txQueue{tasks: make(chan txTask, relayTxQueueSize)}
	for range orgTxQueueWorkers {
		go a.txWorker(a.orgTxQueue)
	}
	for range relayTxQueueWorkers {
		go a.txWorker(a.relayTxQueue)
	}
}

// txWorker runs queued tasks and records each outcome on the job row.
func (a *API) txWorker(q *txQueue) {
	for task := range q.tasks {
		a.runTxTask(task)
	}
}

// runTxTask runs one task and records its outcome, recovering from a panic so a single bad task
// marks its job failed instead of crashing the whole worker pool (and process).
func (a *API) runTxTask(task txTask) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorw(fmt.Errorf("tx task %s panicked: %v", task.jobID, r), "tx task panicked")
			a.recordTxOutcome(task, nil, fmt.Errorf("panic: %v", r))
		}
	}()
	result, err := task.run()
	a.recordTxOutcome(task, result, err)
}

// recordTxOutcome persists the outcome of a finished task, handing over to the task's
// own recorder when it has one.
func (a *API) recordTxOutcome(task txTask, result *db.JobResult, err error) {
	if task.record != nil {
		task.record(result, err)
		return
	}
	if err != nil {
		if e := a.db.SetJobStatus(task.jobID, db.JobStatusFailed, nil, err.Error()); e != nil {
			log.Warnw("could not record failed job", "jobId", task.jobID, "error", e)
		}
		return
	}
	if e := a.db.SetJobStatus(task.jobID, db.JobStatusCompleted, result, ""); e != nil {
		log.Warnw("could not record completed job", "jobId", task.jobID, "error", e)
	}
}

// enqueueTx hands an organization-initiated task (publish, status change, census resize)
// to the org worker pool without blocking. It returns false when the queue is full so the
// caller can respond 503.
func (a *API) enqueueTx(task txTask) bool {
	return a.orgTxQueue.enqueueBatch([]txTask{task})
}

// enqueueRelayTx hands one public vote relay task to the relay worker pool without
// blocking. It returns false when the queue is full so the caller can respond 503.
func (a *API) enqueueRelayTx(task txTask) bool {
	return a.relayTxQueue.enqueueBatch([]txTask{task})
}

// enqueueRelayTxBatch hands a batch of vote relay tasks to the relay worker pool all or
// nothing (see txQueue.enqueueBatch).
func (a *API) enqueueRelayTxBatch(tasks []txTask) bool {
	return a.relayTxQueue.enqueueBatch(tasks)
}
