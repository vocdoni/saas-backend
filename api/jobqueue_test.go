package api

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestOrgTxLockBoundedWait pins that a request waiting on a held organization tx lock gives up
// with ErrOrgTxBusy instead of parking indefinitely: a worker legitimately holds the lock through
// tx mining, so a caller must get a prompt 503 (bounded by orgTxLockTimeout and by its own
// context), and acquire immediately once the lock is free.
func TestOrgTxLockBoundedWait(t *testing.T) {
	c := qt.New(t)
	locks := newOrgTxMutex()
	addr := common.Address{0xaa}

	restore := orgTxLockTimeout
	orgTxLockTimeout = 50 * time.Millisecond
	defer func() { orgTxLockTimeout = restore }()

	held := locks.lock(addr)

	// the bounded wait runs out while the worker holds the lock
	start := time.Now()
	_, err := locks.lockCtx(context.Background(), addr)
	c.Assert(err, qt.Not(qt.IsNil))
	var apiErr errors.Error
	c.Assert(stderrors.As(err, &apiErr), qt.IsTrue)
	c.Assert(apiErr.Code, qt.Equals, errors.ErrOrgTxBusy.Code)
	c.Assert(time.Since(start) < 5*time.Second, qt.IsTrue)

	// a caller whose request context is already gone is refused without waiting
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = locks.lockCtx(ctx, addr)
	c.Assert(stderrors.As(err, &apiErr), qt.IsTrue)
	c.Assert(apiErr.Code, qt.Equals, errors.ErrOrgTxBusy.Code)

	// once the holder releases, acquisition succeeds at once
	held.Unlock()
	got, err := locks.lockCtx(context.Background(), addr)
	c.Assert(err, qt.IsNil)
	got.Unlock()

	// a different organization is never blocked by this one
	other, err := locks.lockCtx(context.Background(), common.Address{0xbb})
	c.Assert(err, qt.IsNil)
	other.Unlock()
}

// TestTxQueueBatchAllOrNothing pins the admission contract of a bounded tx queue: a batch that
// does not fit is refused whole (no partial enqueue), and the service runs two independent
// queues so vote relays and organization txs cannot exhaust each other's slots.
func TestTxQueueBatchAllOrNothing(t *testing.T) {
	c := qt.New(t)
	q := &txQueue{tasks: make(chan txTask, 2)}

	// too big for the queue: refused with nothing enqueued
	c.Assert(q.enqueueBatch(make([]txTask, 3)), qt.IsFalse)
	c.Assert(q.tasks, qt.HasLen, 0)

	// exactly fits
	c.Assert(q.enqueueBatch(make([]txTask, 2)), qt.IsTrue)
	c.Assert(q.tasks, qt.HasLen, 2)

	// full: even a single task is refused
	c.Assert(q.enqueueBatch([]txTask{{}}), qt.IsFalse)

	// the service keeps vote relays and organization txs on separate queues
	c.Assert(testAPI.orgTxQueue == testAPI.relayTxQueue, qt.IsFalse)
	c.Assert(cap(testAPI.orgTxQueue.tasks), qt.Equals, orgTxQueueSize)
	c.Assert(cap(testAPI.relayTxQueue.tasks), qt.Equals, relayTxQueueSize)
}

// TestPublishWorkerKeepsMinedUnpersisted pins the duplicate-election guard of the publish worker:
// when the chain confirms an election but the SetQuestionPublished write fails, the pair is kept
// in minedUnpersisted so later rounds retry only the DB write — the question is never offered for
// resubmission — and a later flush with the DB healthy persists the original election id.
func TestPublishWorkerKeepsMinedUnpersisted(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "minedunpersist12")
	orgAddress := testCreateOrganization(t, token)
	oid := bson.NewObjectID()

	// a question id that does not exist yet: SetQuestionPublished fails like a DB outage would
	q := db.VotingProcessQuestion{ID: bson.NewObjectID(), ProcessID: oid}
	pw := &publishWorker{
		a:                &API{db: testDB},
		vp:               &db.VotingProcess{ID: oid, OrgAddress: orgAddress, InitialStatus: db.QuestionStatusReady},
		questions:        []db.VotingProcessQuestion{q},
		minedUnpersisted: make(map[bson.ObjectID]internal.HexBytes),
	}
	upstream := internal.HexBytes(randomProcessID())

	// the write fails, the pair is retained and the question stays out of the resubmit set
	c.Assert(pw.persistPublished(&pw.questions[0], upstream), qt.IsFalse)
	c.Assert(pw.minedUnpersisted, qt.HasLen, 1)
	c.Assert(pw.flushMinedUnpersisted(), qt.IsFalse)
	c.Assert(pw.questions[0].UpstreamID, qt.HasLen, 0)

	// the DB recovers (the row now exists): the flush persists the original election id
	stored := q
	_, err := testDB.SetQuestion(&stored)
	c.Assert(err, qt.IsNil)
	c.Assert(pw.flushMinedUnpersisted(), qt.IsTrue)
	c.Assert(pw.minedUnpersisted, qt.HasLen, 0)
	got, err := testDB.Question(q.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.UpstreamID.String(), qt.Equals, upstream.String())
	c.Assert(pw.questions[0].UpstreamID.String(), qt.Equals, upstream.String())
}
