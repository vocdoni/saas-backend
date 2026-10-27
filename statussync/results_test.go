package statussync

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	vocdoniapi "go.vocdoni.io/dvote/api"
)

// fakeProcessor records the ids it processed and fails when err is set.
type fakeProcessor struct {
	name string
	err  error

	mu    sync.Mutex
	calls []string
}

func (p *fakeProcessor) Name() string { return p.name }

func (p *fakeProcessor) Process(
	_ context.Context, _ *db.MongoStorage, q *db.VotingProcessQuestion, _ *vocdoniapi.Election,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, q.UpstreamID.String())
	return p.err
}

func (p *fakeProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// fakeChain serves elections by id; a missing id is a read error.
type fakeChain struct {
	mu        sync.Mutex
	elections map[string]*vocdoniapi.Election
}

func (fc *fakeChain) set(id internal.HexBytes, e *vocdoniapi.Election) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.elections[hex.EncodeToString(id)] = e
}

func (fc *fakeChain) election(id []byte) (*vocdoniapi.Election, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	e, ok := fc.elections[hex.EncodeToString(id)]
	if !ok {
		return nil, fmt.Errorf("election %x not found", id)
	}
	return e, nil
}

// newResultsTestSyncer builds a Syncer without DB or chain account: the results queue reads questions
// and elections from fakes, and the processors are the given fakes.
func newResultsTestSyncer(processors ...ResultsProcessor) (*Syncer, *fakeChain) {
	chain := &fakeChain{elections: map[string]*vocdoniapi.Election{}}
	s := New(context.Background(), &Config{ResultsProcessors: processors})
	s.results.question = func(id internal.HexBytes) (*db.VotingProcessQuestion, error) {
		return &db.VotingProcessQuestion{UpstreamID: id}, nil
	}
	s.results.election = chain.election
	return s, chain
}

var (
	finalElection = &vocdoniapi.Election{
		ElectionSummary: vocdoniapi.ElectionSummary{Status: db.QuestionStatusResults, FinalResults: true},
	}
	endedElection    = &vocdoniapi.Election{ElectionSummary: vocdoniapi.ElectionSummary{Status: db.QuestionStatusEnded}}
	readyElection    = &vocdoniapi.Election{ElectionSummary: vocdoniapi.ElectionSummary{Status: db.QuestionStatusReady}}
	resultsElectionA = internal.HexBytes("election-a")
	resultsElectionB = internal.HexBytes("election-b")
)

func TestResultsRunOnlyOnFinalResults(t *testing.T) {
	c := qt.New(t)
	p := &fakeProcessor{name: "p"}
	s, chain := newResultsTestSyncer(p)
	chain.set(resultsElectionA, endedElection)
	chain.set(resultsElectionB, finalElection)

	s.EnqueueResults(resultsElectionA)
	s.EnqueueResults(resultsElectionB)
	c.Assert(s.ProcessPending(), qt.Equals, 0, qt.Commentf("results tasks are not status tasks"))
	c.Assert(p.calls, qt.DeepEquals, []string{resultsElectionB.String()})
	// a plain task whose election is not final is dropped, not retried
	c.Assert(s.results.pending, qt.HasLen, 0)
}

func TestResultsFailingProcessorDoesNotBlockOthers(t *testing.T) {
	c := qt.New(t)
	failing := &fakeProcessor{name: "failing", err: fmt.Errorf("boom")}
	ok := &fakeProcessor{name: "ok"}
	s, chain := newResultsTestSyncer(failing, ok)
	chain.set(resultsElectionA, finalElection)

	s.EnqueueResults(resultsElectionA)
	s.ProcessPending()
	c.Assert(failing.callCount(), qt.Equals, 1)
	c.Assert(ok.callCount(), qt.Equals, 1)
}

func TestResultsDedup(t *testing.T) {
	c := qt.New(t)
	p := &fakeProcessor{name: "p"}
	s, chain := newResultsTestSyncer(p)
	chain.set(resultsElectionA, finalElection)

	s.EnqueueResults(resultsElectionA)
	s.EnqueueResults(resultsElectionA)
	c.Assert(s.results.pending, qt.HasLen, 1)
	s.ProcessPending()
	c.Assert(p.callCount(), qt.Equals, 1)

	// an in-flight id drops new enqueues
	s.results.add(&resultsTask{upstreamID: resultsElectionB})
	taken := s.results.takeDue(true)
	c.Assert(taken, qt.HasLen, 1)
	s.EnqueueResults(resultsElectionB)
	c.Assert(s.results.pending, qt.HasLen, 0)
	s.results.finish(taken[0], resultsNotFinal)

	// awaitFinal never replaces a pending task; a plain task replaces a pending awaitFinal
	key := hex.EncodeToString(resultsElectionB)
	s.EnqueueResults(resultsElectionB)
	s.enqueueAwaitFinal(resultsElectionB)
	c.Assert(s.results.pending[key].awaitFinal, qt.IsFalse)
	delete(s.results.pending, key)
	s.enqueueAwaitFinal(resultsElectionB)
	c.Assert(s.results.pending[key].awaitFinal, qt.IsTrue)
	s.EnqueueResults(resultsElectionB)
	c.Assert(s.results.pending[key].awaitFinal, qt.IsFalse)
}

func TestResultsHolds(t *testing.T) {
	c := qt.New(t)
	failing := &fakeProcessor{name: "failing", err: fmt.Errorf("boom")}
	s, chain := newResultsTestSyncer(failing)
	chain.set(resultsElectionA, finalElection)

	// a failed run holds the id: later enqueues are dropped until the hold expires
	s.EnqueueResults(resultsElectionA)
	s.ProcessPending()
	c.Assert(failing.callCount(), qt.Equals, 1)
	s.EnqueueResults(resultsElectionA)
	s.ProcessPending()
	c.Assert(failing.callCount(), qt.Equals, 1)
	key := hex.EncodeToString(resultsElectionA)
	h, ok := s.results.holds.Peek(key)
	c.Assert(ok, qt.IsTrue)
	c.Assert(h.failures, qt.Equals, 1)
	c.Assert(time.Until(h.until) > 55*time.Second && time.Until(h.until) <= time.Minute, qt.IsTrue)

	// once the hold expires the id runs again, and a second failure doubles the hold
	s.results.holds.Add(key, resultsHold{until: time.Now().Add(-time.Second), failures: h.failures})
	s.EnqueueResults(resultsElectionA)
	s.ProcessPending()
	c.Assert(failing.callCount(), qt.Equals, 2)
	h, _ = s.results.holds.Peek(key)
	c.Assert(h.failures, qt.Equals, 2)
	c.Assert(time.Until(h.until) > 115*time.Second, qt.IsTrue)

	// success resets the failure count and holds for resultsSuccessHold
	failing.err = nil
	s.results.holds.Add(key, resultsHold{until: time.Now().Add(-time.Second), failures: h.failures})
	s.EnqueueResults(resultsElectionA)
	s.ProcessPending()
	c.Assert(failing.callCount(), qt.Equals, 3)
	h, _ = s.results.holds.Peek(key)
	c.Assert(h.failures, qt.Equals, 0)
	c.Assert(time.Until(h.until) > resultsSuccessHold-5*time.Second, qt.IsTrue)
	s.EnqueueResults(resultsElectionA)
	c.Assert(s.results.pending, qt.HasLen, 0)

	// a chain read failure is a failed run too
	s.EnqueueResults(resultsElectionB) // the fake chain does not know it
	s.ProcessPending()
	h, ok = s.results.holds.Peek(hex.EncodeToString(resultsElectionB))
	c.Assert(ok, qt.IsTrue)
	c.Assert(h.failures, qt.Equals, 1)
}

func TestFailureHold(t *testing.T) {
	c := qt.New(t)
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		c.Assert(failureHold(i+1), qt.Equals, w*time.Minute, qt.Commentf("failures=%d", i+1))
	}
}

func TestResultsAwaitFinal(t *testing.T) {
	c := qt.New(t)
	p := &fakeProcessor{name: "p"}
	s, chain := newResultsTestSyncer(p)
	chain.set(resultsElectionA, endedElection)
	key := hex.EncodeToString(resultsElectionA)

	s.enqueueAwaitFinal(resultsElectionA)
	c.Assert(s.results.pending[key].nextAt.After(time.Now()), qt.IsTrue)
	// not final yet: stays pending with one attempt less
	s.ProcessPending()
	c.Assert(p.callCount(), qt.Equals, 0)
	c.Assert(s.results.pending[key].attempts, qt.Equals, awaitFinalAttempts-1)

	// final: runs the processors and is done
	chain.set(resultsElectionA, finalElection)
	s.ProcessPending()
	c.Assert(p.callCount(), qt.Equals, 1)
	c.Assert(s.results.pending, qt.HasLen, 0)

	// a transient read failure does not end the follow-up: it stays pending with one attempt less
	s.enqueueAwaitFinal(resultsElectionB) // the fake chain does not know it yet
	s.ProcessPending()
	c.Assert(s.results.pending[hex.EncodeToString(resultsElectionB)].attempts, qt.Equals, awaitFinalAttempts-1)
	delete(s.results.pending, hex.EncodeToString(resultsElectionB))

	// exhausted: dropped
	chain.set(resultsElectionB, endedElection)
	s.enqueueAwaitFinal(resultsElectionB)
	s.results.pending[hex.EncodeToString(resultsElectionB)].attempts = 1
	s.ProcessPending()
	c.Assert(s.results.pending, qt.HasLen, 0)
}

func TestFollowResults(t *testing.T) {
	c := qt.New(t)
	s, _ := newResultsTestSyncer(&fakeProcessor{name: "p"})
	key := hex.EncodeToString(resultsElectionA)

	s.followResults(resultsElectionA, readyElection)
	c.Assert(s.results.pending, qt.HasLen, 0)

	s.followResults(resultsElectionA, endedElection)
	c.Assert(s.results.pending[key].awaitFinal, qt.IsTrue)

	s.followResults(resultsElectionA, finalElection)
	c.Assert(s.results.pending[key].awaitFinal, qt.IsFalse)
}

func TestResultsDisabledWithoutProcessors(t *testing.T) {
	c := qt.New(t)
	s, _ := newResultsTestSyncer()
	s.EnqueueResults(resultsElectionA)
	s.followResults(resultsElectionB, endedElection)
	c.Assert(s.results.pending, qt.HasLen, 0)

	var nilSyncer *Syncer
	nilSyncer.EnqueueResults(resultsElectionA) // no-op, no panic
}

// TestResultsLoop runs a task through the production loop started by Start.
func TestResultsLoop(t *testing.T) {
	c := qt.New(t)
	p := &fakeProcessor{name: "p"}
	s, chain := newResultsTestSyncer(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.ctx = ctx
	chain.set(resultsElectionA, finalElection)
	s.Start()

	s.EnqueueResults(resultsElectionA)
	deadline := time.Now().Add(5 * time.Second)
	for p.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c.Assert(p.callCount(), qt.Equals, 1)
}
