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
	// otherBallot makes the vote on chain another tx than the relayed one; overwrites is its
	// overwrite count; lookupFails answers every vote lookup with a 500.
	otherBallot bool
	overwrites  uint32
	lookupFails bool

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
		if f.lookupFails {
			http.Error(w, "indexer unavailable", http.StatusInternalServerError)
			return
		}
		if !f.voteOnChain {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		txHash, _ := hex.DecodeString(f.txHash)
		if f.otherBallot {
			other := sha256.Sum256([]byte("another-ballot"))
			txHash = other[:]
		}
		overwrites := f.overwrites
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.Vote{BlockHeight: 7, TxHash: txHash, OverwriteCount: &overwrites})
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
	c.Patch(&voteIndexWait, 10*time.Millisecond)

	// the trailing slash matters: see TestVoteByNullifier
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	node.mu.Lock()
	node.serveInfo = true
	node.mu.Unlock()
	return &Account{client: client}
}

// AC1: a transport error, a 502 and a 504 are retried, and the send that succeeds is mined and
// metered as the nullifier's first vote.
func TestSubmitVoteRetriesTransientErrors(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends:       []sendAnswer{answerTransport, answerStatus(502, ""), answerStatus(504, ""), answerOK},
		minedOnPoll: 1,
		voteOnChain: true,
	}
	a := newFakeVoteNode(c, node)

	relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relay.FirstVote, qt.IsTrue)
	c.Assert(string(relay.VoteID), qt.Equals, "vote-id-from-node")
	sends, _, lookups := node.counts()
	c.Assert(sends, qt.Equals, 4)
	c.Assert(lookups, qt.Equals, 1, qt.Commentf("the mined vote is read back to meter it"))
}

// An overwrite this call landed is a cast vote, but not a first one: it is not metered again.
func TestSubmitVoteOverwriteNotFirst(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerOK}, minedOnPoll: 1, voteOnChain: true, overwrites: 1}
	a := newFakeVoteNode(c, node)

	relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relay.FirstVote, qt.IsFalse)
}

// A mined tx is a cast vote even when the indexer cannot serve it yet: the job succeeds, and the
// vote is not metered rather than metered on a guess.
func TestSubmitVoteMinedButNotIndexed(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerOK}, minedOnPoll: 1, lookupFails: true}
	a := newFakeVoteNode(c, node)

	relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relay.FirstVote, qt.IsFalse)
	_, _, lookups := node.counts()
	c.Assert(lookups, qt.Equals, voteIndexAttempts)
}

// AC2: a definitive error is not retried, and the failure is returned after one nullifier lookup.
func TestSubmitVoteDefinitiveErrorNotRetried(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerStatus(500, "vochain SendTx failed: invalid signature")}}
	a := newFakeVoteNode(c, node)

	_, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `(?s)could not submit vote tx: API error: 500 .*invalid signature.*`)
	sends, polls, lookups := node.counts()
	c.Assert(sends, qt.Equals, 1)
	c.Assert(polls, qt.Equals, 0, qt.Commentf("a definitive answer means the node did not take the tx"))
	c.Assert(lookups, qt.Equals, 1)
}

// AC3: whatever made the send fail, the same tx found on chain by its nullifier is a success. It
// is metered only when a send of this call is known to have been accepted.
func TestSubmitVoteFoundByNullifier(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sends     []sendAnswer
		firstVote bool
	}{
		{"definitive error", []sendAnswer{answerStatus(500, "vote already exists")}, false},
		{"retry budget exhausted", []sendAnswer{answerStatus(502, "")}, false},
		{"mined wait timed out", []sendAnswer{answerOK}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			node := &fakeVoteNode{sends: tc.sends, voteOnChain: true}
			a := newFakeVoteNode(c, node)
			c.Patch(&voteRetryBudget, 100*time.Millisecond)

			relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
			c.Assert(err, qt.IsNil)
			c.Assert(relay.FirstVote, qt.Equals, tc.firstVote)
			c.Assert(relay.VoteID, qt.DeepEquals, testVoteNullifier)
			_, _, lookups := node.counts()
			c.Assert(lookups, qt.Equals, 1)
		})
	}
}

// A rejected ballot whose nullifier holds another vote (an earlier one, with this one refused as
// an overwrite) is a failure: the job must not report a ballot the chain did not take.
func TestSubmitVoteAnotherBallotOnChain(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends:       []sendAnswer{answerStatus(500, "vochain SendTx failed: overwrite count reached")},
		voteOnChain: true,
		otherBallot: true,
	}
	a := newFakeVoteNode(c, node)

	_, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `(?s)could not submit vote tx: .*overwrite count reached.*another vote under its nullifier.*`)
}

// "already exists in cache" on the very first send: someone relayed these bytes before this call.
// The vote is waited for and confirmed, but not metered (that relay landed it).
func TestSubmitVoteInCacheOnFirstSend(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends:       []sendAnswer{answerStatus(500, "vochain SendTx failed: tx already exists in cache")},
		minedOnPoll: 1,
		voteOnChain: true,
	}
	a := newFakeVoteNode(c, node)

	relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relay.FirstVote, qt.IsFalse)
	c.Assert(relay.VoteID, qt.DeepEquals, testVoteNullifier)
	sends, _, lookups := node.counts()
	c.Assert(sends, qt.Equals, 1)
	c.Assert(lookups, qt.Equals, 0)
}

// AC3b: a lost answer followed by "already exists in cache" means the first send was accepted:
// SubmitVote waits for the locally computed tx hash, sends nothing more, and meters the vote.
func TestSubmitVoteInCacheIsAccepted(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{
		sends: []sendAnswer{
			answerTransport,
			answerStatus(500, `{"error":"vochain SendTx failed: tx already exists in cache","code":5001}`),
			answerOK, // must never be reached
		},
		minedOnPoll: 2,
		voteOnChain: true,
	}
	a := newFakeVoteNode(c, node)
	// the second poll comes one apiclient.PollInterval (5s) after the first
	c.Patch(&voteMinedTimeout, 10*time.Second)

	relay, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.IsNil)
	c.Assert(relay.FirstVote, qt.IsTrue)
	c.Assert(relay.VoteID, qt.DeepEquals, testVoteNullifier)
	sends, polls, lookups := node.counts()
	c.Assert(sends, qt.Equals, 2)
	c.Assert(polls, qt.Equals, 2)
	c.Assert(lookups, qt.Equals, 1)
}

// AC3c: a 503 has already been retried by dvote's client (apiclient.DefaultRetries sends, one
// block apart); the outer loop does not add more, and the failure goes to the nullifier lookup.
func TestSubmitVoteServiceUnavailableNotRetried(t *testing.T) {
	c := qt.New(t)
	node := &fakeVoteNode{sends: []sendAnswer{answerStatus(503, "mempool is full")}}
	a := newFakeVoteNode(c, node)

	_, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
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

	_, err := a.SubmitVote(context.Background(), testVoteTx, testVoteNullifier)
	c.Assert(err, qt.ErrorMatches, `(?s)could not submit vote tx: Post ".*/chain/transactions": .*EOF.*`)
	sends, polls, lookups := node.counts()
	c.Assert(sends > 1, qt.IsTrue, qt.Commentf("sends: %d", sends))
	c.Assert(polls > 0, qt.IsTrue, qt.Commentf("the ambiguous send must be waited for by hash"))
	c.Assert(lookups, qt.Equals, 1)
}

// A connection that cannot even be opened is retried like any transport error, but proves the tx
// never reached the node, so it is not waited for by hash; any other transport error may have.
func TestSendNeverLeft(t *testing.T) {
	c := qt.New(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	closedURL := srv.URL
	srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, closedURL, strings.NewReader("{}"))
	c.Assert(err, qt.IsNil)
	_, err = http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNotNil)
	c.Assert(sendNeverLeft(err), qt.IsTrue, qt.Commentf("error: %v", err))
	c.Assert(transientSendErr(err), qt.IsTrue)

	c.Assert(sendNeverLeft(fmt.Errorf(`Post "http://node/chain/transactions": EOF`)), qt.IsFalse)
	c.Assert(sendNeverLeft(fmt.Errorf("API error: 502 ()")), qt.IsFalse)
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
