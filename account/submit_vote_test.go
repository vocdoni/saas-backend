package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
)

// sendAnswer scripts one answer of the fake node to POST /chain/transactions.
type sendAnswer func(w http.ResponseWriter)

var (
	// answerTransport drops the connection without an HTTP answer: the client sees EOF.
	answerTransport sendAnswer = func(http.ResponseWriter) { panic(http.ErrAbortHandler) }
	answerOK        sendAnswer = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.Transaction{Response: []byte("vote-id-from-node")})
	}
)

func answerStatus(code int, body string) sendAnswer {
	return func(w http.ResponseWriter) { http.Error(w, body, code) }
}

// fakeVoteNode is a scripted Vochain API: the i-th send gets sends[i] (the last answer repeats),
// the tx reference of txHash is found from poll minedOnPoll on (0: never), and the vote of
// nullifier exists when voteOnChain. Once serveInfo is set, /chain/info reports a height that
// grows on every call, so dvote's wait for the next block after a 503 returns at once.
type fakeVoteNode struct {
	mu          sync.Mutex
	serveInfo   bool
	height      uint32
	sends       []sendAnswer
	txHash      string
	minedOnPoll int
	nullifier   string
	voteOnChain bool

	sendCount, pollCount, lookupCount int
}

func (f *fakeVoteNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/chain/transactions":
		answer := f.sends[min(f.sendCount, len(f.sends)-1)]
		f.sendCount++
		answer(w)
	case r.URL.Path == "/chain/transactions/reference/"+f.txHash:
		f.pollCount++
		if f.minedOnPoll == 0 || f.pollCount < f.minedOnPoll {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.TransactionReference{Height: 7})
	case r.URL.Path == "/votes/"+f.nullifier:
		f.lookupCount++
		if !f.voteOnChain {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.Vote{BlockHeight: 7})
	case r.URL.Path == "/chain/info" && f.serveInfo:
		f.height++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.ChainInfo{Height: f.height})
	default: // including the /chain/info probe apiclient.New makes
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (f *fakeVoteNode) counts() (sends, polls, lookups int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendCount, f.pollCount, f.lookupCount
}

var (
	testVoteTx        = []byte("signed-vote-tx")
	testVoteNullifier = []byte(strings.Repeat("n", 32))
)

// newFakeVoteNode starts a fake node for testVoteTx/testVoteNullifier and returns an Account
// talking to it. The retry knobs are shortened for the test and restored afterwards.
func newFakeVoteNode(c *qt.C, node *fakeVoteNode) *Account {
	hash := sha256.Sum256(testVoteTx)
	node.txHash = hex.EncodeToString(hash[:])
	node.nullifier = hex.EncodeToString(testVoteNullifier)
	srv := httptest.NewServer(node)
	c.Cleanup(srv.Close)

	c.Patch(&voteRetryBase, 10*time.Millisecond)
	c.Patch(&voteRetryCap, 40*time.Millisecond)
	c.Patch(&voteRetryBudget, 2*time.Second)
	c.Patch(&voteMinedTimeout, 200*time.Millisecond)

	// the trailing slash matters: see TestVoteByNullifier
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	node.mu.Lock()
	node.serveInfo = true
	node.mu.Unlock()
	return &Account{client: client}
}

// AC1: a transport error, a 502 and a 504 are retried, and the send that succeeds is mined.
func TestSubmitVoteRetriesTransientErrors(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends:       []sendAnswer{answerTransport, answerStatus(502, ""), answerStatus(504, ""), answerOK},
		minedOnPoll: 1,
	}
	a := newFakeVoteNode(c, node)

	voteID, relayedBefore, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relayedBefore, qt.IsFalse)
	c.Assert(string(voteID), qt.Equals, "vote-id-from-node")
	sends, _, lookups := node.counts()
	c.Assert(sends, qt.Equals, 4)
	c.Assert(lookups, qt.Equals, 0)
}

// AC2: a definitive error is not retried, and the failure is returned after one nullifier lookup.
func TestSubmitVoteDefinitiveErrorNotRetried(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerStatus(500, "vochain SendTx failed: invalid signature")}}
	a := newFakeVoteNode(c, node)

	_, _, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `(?s)could not submit vote tx: API error: 500 .*invalid signature.*`)
	sends, polls, lookups := node.counts()
	c.Assert(sends, qt.Equals, 1)
	c.Assert(polls, qt.Equals, 0, qt.Commentf("a definitive answer means the node did not take the tx"))
	c.Assert(lookups, qt.Equals, 1)
}

// AC3: whatever made the send fail, a vote found on chain by its nullifier is a success. It is
// reported as relayed before unless a send of this call was accepted (then the vote is ours,
// only its mined wait timed out).
func TestSubmitVoteFoundByNullifier(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sends         []sendAnswer
		relayedBefore bool
	}{
		{"definitive error", []sendAnswer{answerStatus(500, "overwrite count reached")}, true},
		{"retry budget exhausted", []sendAnswer{answerStatus(502, "")}, true},
		{"mined wait timed out", []sendAnswer{answerOK}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			node := &fakeVoteNode{sends: tc.sends, voteOnChain: true}
			a := newFakeVoteNode(c, node)
			c.Patch(&voteRetryBudget, 100*time.Millisecond)

			voteID, relayedBefore, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
			c.Assert(err, qt.IsNil)
			c.Assert(relayedBefore, qt.Equals, tc.relayedBefore)
			c.Assert(voteID, qt.DeepEquals, testVoteNullifier)
			_, _, lookups := node.counts()
			c.Assert(lookups, qt.Equals, 1)
		})
	}
}

// "already exists in cache" on the very first send: someone relayed these bytes before this call.
// The vote is waited for and confirmed, but not claimed (no double metering of a re-relay).
func TestSubmitVoteInCacheOnFirstSend(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends:       []sendAnswer{answerStatus(500, "vochain SendTx failed: tx already exists in cache")},
		minedOnPoll: 1,
	}
	a := newFakeVoteNode(c, node)

	voteID, relayedBefore, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relayedBefore, qt.IsTrue)
	c.Assert(voteID, qt.DeepEquals, testVoteNullifier)
	sends, _, lookups := node.counts()
	c.Assert(sends, qt.Equals, 1)
	c.Assert(lookups, qt.Equals, 0)
}

// AC3b: a lost answer followed by "already exists in cache" means the first send was accepted:
// SubmitVote waits for the locally computed tx hash, and sends nothing more.
func TestSubmitVoteInCacheIsAccepted(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends: []sendAnswer{
			answerTransport,
			answerStatus(500, `{"error":"vochain SendTx failed: tx already exists in cache","code":5001}`),
			answerOK, // must never be reached
		},
		minedOnPoll: 2,
	}
	a := newFakeVoteNode(c, node)
	// the second poll comes one apiclient.PollInterval (5s) after the first
	c.Patch(&voteMinedTimeout, 10*time.Second)

	voteID, relayedBefore, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relayedBefore, qt.IsFalse)
	c.Assert(voteID, qt.DeepEquals, testVoteNullifier)
	sends, polls, lookups := node.counts()
	c.Assert(sends, qt.Equals, 2)
	c.Assert(polls, qt.Equals, 2)
	c.Assert(lookups, qt.Equals, 0)
}

// AC3c: a 503 has already been retried by dvote's client (apiclient.DefaultRetries sends, one
// block apart); the outer loop does not add more, and the failure goes to the nullifier lookup.
func TestSubmitVoteServiceUnavailableNotRetried(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerStatus(503, "mempool is full")}}
	a := newFakeVoteNode(c, node)

	_, _, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `could not submit vote tx: .*read on closed response body`)
	sends, polls, lookups := node.counts()
	c.Assert(sends, qt.Equals, apiclient.DefaultRetries)
	c.Assert(polls, qt.Equals, 0, qt.Commentf("a full mempool did not take the tx"))
	c.Assert(lookups, qt.Equals, 1)
}

// AC4: when the budget runs out on transport errors, the failure names the last of them, and the
// tx is still waited for by hash before the nullifier lookup (the node may hold it).
func TestSubmitVoteBudgetExhausted(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerTransport}}
	a := newFakeVoteNode(c, node)
	c.Patch(&voteRetryBudget, 150*time.Millisecond)

	_, relayedBefore, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `(?s)could not submit vote tx: Post ".*/chain/transactions": .*EOF.*`)
	c.Assert(relayedBefore, qt.IsFalse)
	sends, polls, lookups := node.counts()
	c.Assert(sends > 1, qt.IsTrue, qt.Commentf("sends: %d", sends))
	c.Assert(polls > 0, qt.IsTrue, qt.Commentf("the ambiguous send must be waited for by hash"))
	c.Assert(lookups, qt.Equals, 1)
}

// sendTxStatus parses the status out of dvote's error string, and only out of it.
func TestSendTxStatus(t *testing.T) {
	c := qt.New(t)
	for msg, want := range map[string]int{
		"API error: 502 ()":                           502,
		"API error: 500 (tx already exists in cache)": 500,
		`Post "http://node/chain/transactions": EOF`:  0,
		"API error: notanumber":                       0,
	} {
		c.Assert(sendTxStatus(fmt.Errorf("%s", msg)), qt.Equals, want, qt.Commentf("%q", msg))
	}
}
