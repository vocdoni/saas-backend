package api

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// orgTxMutex hands out a per-organization mutex so the build->sign->submit pipeline for
// backend-submitted txs (publish and status change) is serialized per org. Two concurrent
// such requests for the same org would otherwise read the same account nonce and sign
// conflicting transactions. In-process only — a multi-instance deployment would
// need a distributed lock, matching the single-instance assumption of db.keysLock. The
// locks map grows unbounded; an org count high enough to matter is not realistic here.
type orgTxMutex struct {
	mu    sync.Mutex
	locks map[common.Address]*sync.Mutex
}

func newOrgTxMutex() *orgTxMutex {
	return &orgTxMutex{locks: make(map[common.Address]*sync.Mutex)}
}

// lock acquires and returns the mutex for addr. Because submit runs on a worker, the
// caller hands the returned mutex to the worker, which Unlocks it after the on-chain
// submit completes — the lock is therefore held across the async hand-off.
func (o *orgTxMutex) lock(addr common.Address) *sync.Mutex {
	o.mu.Lock()
	m, ok := o.locks[addr]
	if !ok {
		m = &sync.Mutex{}
		o.locks[addr] = m
	}
	o.mu.Unlock()
	m.Lock()
	return m
}

// pool sizes are consts; promote to config only if tuning is needed.
const (
	// txQueueSize bounds the number of queued-but-not-yet-running tx tasks. It is sized to hold
	// several full vote batches at once: POST /votes reserves one slot per envelope all-or-nothing,
	// so a queue merely as large as maxVotesPerBatch would accept a full batch only on a completely
	// idle service, and a client turned away falls back to relaying one vote at a time — the
	// half-voted window that endpoint exists to close. The buffer is not the bottleneck (the
	// workers below are), so a bigger one adds no chain load or concurrency; it only stops
	// rejecting work the service can serve. A txTask is a string and two func pointers, so this
	// costs tens of kilobytes.
	txQueueSize = 512
	// txQueueWorkers caps concurrent on-chain submits so a chain stall cannot drain
	// the router's shared request budget or starve the public CSP voter path.
	txQueueWorkers = 16
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

// startTxQueue creates the buffered queue and launches the worker pool. Called once
// from New(). No graceful drain — on process exit in-flight tasks die and
// their jobs stay `pending`; add a Stop()/drain only if that ceiling starts to bite.
func (a *API) startTxQueue() {
	a.txQueue = make(chan txTask, txQueueSize)
	for range txQueueWorkers {
		go a.txWorker()
	}
}

// txWorker runs queued tasks and records each outcome on the job row.
func (a *API) txWorker() {
	for task := range a.txQueue {
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

// enqueueTx hands a task to the worker pool without blocking. It returns false when
// the queue is full so the caller can respond 503.
func (a *API) enqueueTx(task txTask) bool {
	return a.enqueueTxBatch([]txTask{task})
}

// enqueueTxBatch hands a group of tasks to the worker pool all or nothing: it returns
// false, having enqueued none of them, when the queue cannot take the whole group. A
// caller relaying several votes at once therefore never leaves a voter half-voted
// because the queue filled up midway. The mutex makes the free-slot check and the sends
// atomic against each other; workers only ever drain the queue, so no concurrent
// producer can invalidate a check taken under the lock.
func (a *API) enqueueTxBatch(tasks []txTask) bool {
	a.txQueueMu.Lock()
	defer a.txQueueMu.Unlock()

	if cap(a.txQueue)-len(a.txQueue) < len(tasks) {
		return false
	}
	for _, task := range tasks {
		a.txQueue <- task
	}
	return true
}
