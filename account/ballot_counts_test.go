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
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
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

var fakeElectionID = []byte(strings.Repeat("e", 32))

// fakeVoteNode serves the votes list of fakeElectionID, one page per entry of pages, an empty page past
// the last one, and each listed vote by its nullifier. It returns an Account bound to it.
func fakeVoteNode(t *testing.T, pages ...[]fakeVote) *Account {
	t.Helper()
	votes := map[string]fakeVote{}
	lists := make([]api.VotesList, len(pages))
	for p, page := range pages {
		lists[p].Votes = []*api.Vote{}
		for i, v := range page {
			nullifier := []byte(fmt.Sprintf("%031d%d", p, i))
			votes[hex.EncodeToString(nullifier)] = v
			lists[p].Votes = append(lists[p].Votes, &api.Vote{VoteID: nullifier})
		}
	}
	pagePrefix := "/elections/" + hex.EncodeToString(fakeElectionID) + "/votes/page/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		switch {
		case strings.HasPrefix(r.URL.Path, pagePrefix):
			p, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, pagePrefix))
			if err != nil {
				http.Error(w, "bad page", http.StatusBadRequest)
				return
			}
			body = api.VotesList{Votes: []*api.Vote{}}
			if p < len(lists) {
				body = lists[p]
			}
		case strings.HasPrefix(r.URL.Path, "/votes/"):
			id := strings.TrimPrefix(r.URL.Path, "/votes/")
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
		default: // including the /chain/info probe apiclient.New makes
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := apiclient.New(srv.URL + "/")
	qt.Assert(t, err, qt.IsNil)
	return &Account{client: client}
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
