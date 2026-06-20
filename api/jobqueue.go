package api

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// orgTxMutex hands out a per-organization mutex so the build->sign->submit pipeline for
// backend-submitted txs (publish and status change) is serialized per org. Two concurrent
// such requests for the same org would otherwise read the same account nonce and sign
// conflicting transactions. ponytail: in-process only — a multi-instance deployment would
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

// ponytail: pool sizes are consts; promote to config only if tuning is needed.
const (
	// txQueueSize bounds the number of queued-but-not-yet-running tx tasks.
	txQueueSize = 100
	// txQueueWorkers caps concurrent on-chain submits so a chain stall cannot drain
	// the router's shared request budget or starve the public CSP voter path.
	txQueueWorkers = 16
)

// txTask is a unit of background transaction work. run performs the on-chain submit
// plus any post-submit DB writes, returning the job result on success or an error on
// failure. The worker records the terminal outcome via db.SetJobStatus.
type txTask struct {
	jobID string
	run   func() (*db.JobResult, error)
}

// startTxQueue creates the buffered queue and launches the worker pool. Called once
// from New(). ponytail: no graceful drain — on process exit in-flight tasks die and
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
		result, err := task.run()
		if err != nil {
			if e := a.db.SetJobStatus(task.jobID, db.JobStatusFailed, nil, err.Error()); e != nil {
				log.Warnw("could not record failed job", "jobId", task.jobID, "error", e)
			}
			continue
		}
		if e := a.db.SetJobStatus(task.jobID, db.JobStatusCompleted, result, ""); e != nil {
			log.Warnw("could not record completed job", "jobId", task.jobID, "error", e)
		}
	}
}

// enqueueTx hands a task to the worker pool without blocking. It returns false when
// the queue is full so the caller can respond 503.
func (a *API) enqueueTx(task txTask) bool {
	select {
	case a.txQueue <- task:
		return true
	default:
		return false
	}
}
