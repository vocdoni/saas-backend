package account

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/api"
)

// fakeVote is one vote served by fakeVoteNode: its package (nil = omitted) and weight ("" = omitted).
// missing lists the vote in its page but answers 404 for it.
type fakeVote struct {
	pkg     string
	weight  string
	missing bool
	// keys marks the vote as encrypted (key indexes present)
	keys bool
}

const fakeVotesPath = "/v2/votes"

var fakeElectionID = []byte(strings.Repeat("e", 32))

// fakeNode records the requests a fakeVoteNode receives and can inject failures.
type fakeNode struct {
	mu     sync.Mutex
	hits   map[string]int // requests per path+query
	limits []string       // limit param of every list request
	// fail, when set, returns an HTTP status to answer a request with instead of serving it (0 = serve);
	// n is the 1-based count of requests made for the same path+query.
	fail func(r *http.Request, n int) int
}

func (f *fakeNode) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := 0
	for _, n := range f.hits {
		sum += n
	}
	return sum
}

// fakeVoteNode serves the votes list of fakeElectionID, 100-row-capped pages as given by pages, 404 past
// the last one, and each listed vote by its nullifier. It returns an Account whose vote reader is bound to it.
func fakeVoteNode(t *testing.T, pages ...[]fakeVote) *Account {
	t.Helper()
	a, _ := fakeVoteNodeCtl(t, pages...)
	return a
}

// fakeVoteNodeCtl is fakeVoteNode that also returns the node's request recorder.
func fakeVoteNodeCtl(t *testing.T, pages ...[]fakeVote) (*Account, *fakeNode) {
	t.Helper()
	votes := map[string]fakeVote{}
	lists := make([]api.VotesList, len(pages))
	var total uint64
	for _, page := range pages {
		total += uint64(len(page))
	}
	for p, page := range pages {
		lists[p].Votes = []*api.Vote{}
		lists[p].Pagination = &api.Pagination{TotalItems: total, CurrentPage: uint64(p), LastPage: uint64(len(pages) - 1)}
		for i, v := range page {
			nullifier := []byte(fmt.Sprintf("%031d%d", p, i))
			votes[hex.EncodeToString(nullifier)] = v
			lists[p].Votes = append(lists[p].Votes, &api.Vote{VoteID: nullifier})
		}
	}
	node := &fakeNode{hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node.mu.Lock()
		key := r.URL.Path + "?" + r.URL.RawQuery
		node.hits[key]++
		n := node.hits[key]
		if r.URL.Path == fakeVotesPath {
			node.limits = append(node.limits, r.URL.Query().Get("limit"))
		}
		fail := node.fail
		node.mu.Unlock()
		if fail != nil {
			if code := fail(r, n); code != 0 {
				http.Error(w, "injected failure", code)
				return
			}
		}
		var body any
		switch {
		case r.URL.Path == fakeVotesPath:
			q := r.URL.Query()
			p, err := strconv.Atoi(q.Get("page"))
			if err != nil || q.Get("electionId") != hex.EncodeToString(fakeElectionID) {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if p < 0 || p >= len(lists) {
				http.Error(w, "page not found", http.StatusNotFound)
				return
			}
			body = lists[p]
		case strings.HasPrefix(r.URL.Path, "/v2/votes/"):
			id := strings.TrimPrefix(r.URL.Path, "/v2/votes/")
			v, ok := votes[id]
			if !ok || v.missing {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			vote := &api.Vote{VoteWeight: v.weight}
			if v.keys {
				vote.EncryptionKeyIndexes = []uint32{0}
			}
			if v.pkg != "" {
				vote.VotePackage = json.RawMessage(v.pkg)
			}
			body = vote
		default:
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	// a base path, to check it is kept
	reader, err := newVoteReader(srv.URL + "/v2")
	qt.Assert(t, err, qt.IsNil)
	old := voteReadBackoff
	voteReadBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { voteReadBackoff = old })
	return &Account{votes: reader}, node
}

// multiChoiceClassifier classifies a 3-choice multichoice question allowing up to 2 selections.
func multiChoiceClassifier(t *testing.T) BallotClassifier {
	t.Helper()
	c, err := NewBallotClassifier(&db.VotingProcessQuestion{
		Type: db.VotingTypeMultiChoice, Choices: classifierChoices(3),
		TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
	})
	qt.Assert(t, err, qt.IsNil)
	return c
}

// TestElectionBallotCounts counts a mix of blank, invalid and valid ballots with weights other than 1
// across several pages; undecodable packages are invalid, not an abort.
func TestElectionBallotCounts(t *testing.T) {
	c := qt.New(t)
	a := fakeVoteNode(t,
		[]fakeVote{
			{pkg: `{"votes":[0,0,0]}`, weight: "2"}, // blank
			{pkg: `{"votes":[]}`, weight: "1"},      // blank
			{pkg: `{"votes":[1,0,1]}`, weight: "3"}, // selects 0 and 2
		},
		[]fakeVote{
			{pkg: `["0"]`, weight: "5"},             // not an object: invalid
			{pkg: `{}`},                             // no votes key: an empty ballot, blank; weight defaults to 1
			{pkg: `{"votes":[1,1,1]}`, weight: "4"}, // over maxTotalCost 2: invalid
		},
		[]fakeVote{
			{pkg: `{"votes":[0,1,0]}`, weight: "7"}, // selects 1
		},
	)
	counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 7, multiChoiceClassifier(t))
	c.Assert(err, qt.IsNil)
	c.Assert(counts, qt.DeepEquals, &db.BallotCounts{
		BlankVoteCount:    3,
		BlankVoteWeight:   "4",
		InvalidVoteCount:  2,
		InvalidVoteWeight: "9",
		OptionVoteCounts:  []uint64{1, 1, 1},
		OptionVoteWeights: []string{"3", "7", "3"},
	})

	// no per-option arrays for a layout without options (ranked)
	ranked, err := NewBallotClassifier(&db.VotingProcessQuestion{Type: db.VotingTypeRanked, Choices: classifierChoices(3)})
	c.Assert(err, qt.IsNil)
	a = fakeVoteNode(t, []fakeVote{{pkg: `{"votes":[2,1,0]}`}, {pkg: `{"votes":[]}`, weight: "2"}})
	counts, err = a.ElectionBallotCounts(context.Background(), fakeElectionID, 2, ranked)
	c.Assert(err, qt.IsNil)
	c.Assert(counts, qt.DeepEquals, &db.BallotCounts{
		BlankVoteCount: 1, BlankVoteWeight: "2", InvalidVoteWeight: "0",
	})
}

// TestElectionBallotCountsAborts: anything that means a ballot could not be read aborts the whole
// computation instead of returning wrong counts.
func TestElectionBallotCountsAborts(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ok := fakeVote{pkg: `{"votes":[1,0,0]}`}
	tests := []struct {
		name      string
		ctx       context.Context
		votes     []fakeVote
		voteCount uint64
		want      string
	}{
		{"listed vote not found", context.Background(), []fakeVote{ok, {missing: true}}, 2, ".*vote not found.*"},
		{
			"unparsable weight", context.Background(),
			[]fakeVote{{pkg: `{"votes":[]}`, weight: "x"}},
			1, ".*unparsable weight.*",
		},
		{"rows differ from voteCount", context.Background(), []fakeVote{ok, ok}, 3, ".*2 votes listed, election reports 3.*"},
		{"cancelled context", cancelled, []fakeVote{ok}, 1, ".*context canceled.*"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			a := fakeVoteNode(t, tc.votes)
			counts, err := a.ElectionBallotCounts(tc.ctx, fakeElectionID, tc.voteCount, multiChoiceClassifier(t))
			c.Assert(err, qt.ErrorMatches, tc.want)
			c.Assert(counts, qt.IsNil)
		})
	}
}

// TestElectionBallotCountsUnreadable: a voter cannot block the counts with an empty or undecryptable package.
func TestElectionBallotCountsUnreadable(t *testing.T) {
	c := qt.New(t)
	a := fakeVoteNode(t, []fakeVote{
		{pkg: `{"votes":[1,0,0]}`, weight: "1"},
		{weight: "2"},
		{pkg: `{"encrypted":"AQID"}`, weight: "3", keys: true},
	})
	counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 3, multiChoiceClassifier(t))
	c.Assert(err, qt.IsNil)
	c.Assert(counts, qt.DeepEquals, &db.BallotCounts{
		BlankVoteWeight:   "0",
		InvalidVoteCount:  2,
		InvalidVoteWeight: "5",
		OptionVoteCounts:  []uint64{1, 0, 0},
		OptionVoteWeights: []string{"1", "0", "0"},
	})
}

// TestDecodeVotePackage pins which packages decode and which the chain skips as invalid.
func TestDecodeVotePackage(t *testing.T) {
	tests := []struct {
		pkg   string
		keys  []uint32
		votes []int
		ok    bool
	}{
		{`{"votes":[1,0]}`, nil, []int{1, 0}, true},
		{`{"votes":[]}`, nil, []int{}, true},
		{`{"Votes":[2]}`, nil, []int{2}, true},
		{`{"nonce":"n","votes":[1]}`, nil, []int{1}, true},
		{`{}`, nil, nil, true},
		{`null`, nil, nil, true},
		{`{"votes":null}`, nil, nil, true},
		{`{"encrypted":"AQID"}`, nil, nil, true},
		{`{"encrypted":"AQID"}`, []uint32{0}, nil, false},
		{`{"encrypted":null}`, nil, nil, false},
		{`["0"]`, nil, nil, false},
		{`{"votes":[1,"x"]}`, nil, nil, false},
		{`{"nonce":1,"votes":[1]}`, nil, nil, false},
		{`"text"`, nil, nil, false},
		{``, nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.pkg, func(t *testing.T) {
			c := qt.New(t)
			votes, ok := decodeVotePackage(&api.Vote{VotePackage: json.RawMessage(tc.pkg), EncryptionKeyIndexes: tc.keys})
			c.Assert(ok, qt.Equals, tc.ok)
			c.Assert(votes, qt.DeepEquals, tc.votes)
		})
	}
}

// TestElectionBallotCountsRetries: a transient failure of any read is retried and the run succeeds; a
// persistent one aborts after exactly voteReadAttempts attempts.
func TestElectionBallotCountsRetries(t *testing.T) {
	pageKey := func(r *http.Request) bool { return r.URL.Path == fakeVotesPath }
	voteKey := func(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/v2/votes/") }
	ok := []fakeVote{{pkg: `{"votes":[1,0,0]}`}, {pkg: `{"votes":[0,1,0]}`}}
	tests := []struct {
		name string
		fail func(r *http.Request, n int) int
	}{
		{"vote fails with 500 once", func(r *http.Request, n int) int {
			if voteKey(r) && n == 1 {
				return http.StatusInternalServerError
			}
			return 0
		}},
		{"page fails with 503 once", func(r *http.Request, n int) int {
			if pageKey(r) && n == 1 {
				return http.StatusServiceUnavailable
			}
			return 0
		}},
		{"page is rate limited once", func(r *http.Request, n int) int {
			if pageKey(r) && n == 1 {
				return http.StatusTooManyRequests
			}
			return 0
		}},
		{"vote 404s once", func(r *http.Request, n int) int {
			if voteKey(r) && n == 1 {
				return http.StatusNotFound
			}
			return 0
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			a, node := fakeVoteNodeCtl(t, ok)
			node.fail = tc.fail
			counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 2, multiChoiceClassifier(t))
			c.Assert(err, qt.IsNil)
			c.Assert(counts.OptionVoteCounts, qt.DeepEquals, []uint64{1, 1, 0})
		})
	}

	t.Run("persistent 500 on a vote", func(t *testing.T) {
		c := qt.New(t)
		a, node := fakeVoteNodeCtl(t, ok[:1])
		node.fail = func(r *http.Request, _ int) int {
			if voteKey(r) {
				return http.StatusInternalServerError
			}
			return 0
		}
		counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 1, multiChoiceClassifier(t))
		c.Assert(err, qt.ErrorMatches, "(?s).*status 500.*")
		c.Assert(counts, qt.IsNil)
		node.mu.Lock()
		defer node.mu.Unlock()
		for k, n := range node.hits {
			if strings.HasPrefix(k, "/v2/votes/") {
				c.Assert(n, qt.Equals, voteReadAttempts)
			}
		}
	})

	t.Run("persistent 404 on a vote", func(t *testing.T) {
		c := qt.New(t)
		a, node := fakeVoteNodeCtl(t, []fakeVote{{missing: true}})
		_, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 1, multiChoiceClassifier(t))
		c.Assert(err, qt.ErrorIs, ErrVoteNotFound)
		c.Assert(node.total(), qt.Equals, 1+voteReadAttempts)
	})

	t.Run("a non-retryable status is not retried", func(t *testing.T) {
		c := qt.New(t)
		a, node := fakeVoteNodeCtl(t, ok)
		node.fail = func(r *http.Request, _ int) int {
			if pageKey(r) {
				return http.StatusBadRequest
			}
			return 0
		}
		_, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 2, multiChoiceClassifier(t))
		c.Assert(err, qt.ErrorMatches, "(?s).*status 400.*")
		c.Assert(node.total(), qt.Equals, 1)
	})
}

// TestElectionBallotCountsNoVotes: an election without votes yields zero counts without any request.
func TestElectionBallotCountsNoVotes(t *testing.T) {
	c := qt.New(t)
	a, node := fakeVoteNodeCtl(t)
	counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 0, multiChoiceClassifier(t))
	c.Assert(err, qt.IsNil)
	c.Assert(counts, qt.DeepEquals, &db.BallotCounts{
		BlankVoteWeight:   "0",
		InvalidVoteWeight: "0",
		OptionVoteCounts:  []uint64{0, 0, 0},
		OptionVoteWeights: []string{"0", "0", "0"},
	})
	c.Assert(node.total(), qt.Equals, 0)
}

// TestElectionBallotCountsManyPages: the list is requested 100 rows at a time and every page is read.
func TestElectionBallotCountsManyPages(t *testing.T) {
	c := qt.New(t)
	page := func(n int) []fakeVote {
		out := make([]fakeVote, n)
		for i := range out {
			out[i] = fakeVote{pkg: `{"votes":[1,0,0]}`, weight: "2"}
		}
		return out
	}
	a, node := fakeVoteNodeCtl(t, page(100), page(100), page(30))
	counts, err := a.ElectionBallotCounts(context.Background(), fakeElectionID, 230, multiChoiceClassifier(t))
	c.Assert(err, qt.IsNil)
	c.Assert(counts.OptionVoteCounts, qt.DeepEquals, []uint64{230, 0, 0})
	c.Assert(counts.OptionVoteWeights[0], qt.Equals, "460")
	node.mu.Lock()
	defer node.mu.Unlock()
	c.Assert(node.limits, qt.HasLen, 3)
	for _, l := range node.limits {
		c.Assert(l, qt.Equals, "100")
	}
}
