package account

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
)

// fakeVotesNode serves the node's GET /votes?electionId=&page= list from the given pages and
// GET /votes/{id} from the given packages, answering a page past the last one with the node's
// 404 "page not found". It records every list page requested.
func fakeVotesNode(t *testing.T, electionID []byte, pages [][]*api.Vote,
	packages map[string]string, requested *[]int,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/votes":
			if got := r.URL.Query().Get("electionId"); got != hex.EncodeToString(electionID) {
				t.Errorf("electionId query param: got %q", got)
			}
			page, err := strconv.Atoi(r.URL.Query().Get("page"))
			if err != nil {
				t.Errorf("page query param: %v", err)
			}
			*requested = append(*requested, page)
			if page >= len(pages) && (page != 0 || len(pages) != 0) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"page not found","code":4057}`))
				return
			}
			var votes []*api.Vote
			if page < len(pages) {
				votes = pages[page]
			}
			last := uint64(max(len(pages)-1, 0))
			p := &api.Pagination{CurrentPage: uint64(page), LastPage: last}
			if uint64(page) < last {
				next := uint64(page + 1)
				p.NextPage = &next
			}
			if err := json.NewEncoder(w).Encode(&api.VotesList{Votes: votes, Pagination: p}); err != nil {
				t.Errorf("encoding votes list: %v", err)
			}
		case strings.HasPrefix(r.URL.Path, "/votes/"):
			pkg, ok := packages[strings.TrimPrefix(r.URL.Path, "/votes/")]
			if !ok {
				http.Error(w, `{"error":"vote not found"}`, http.StatusNotFound)
				return
			}
			if err := json.NewEncoder(w).Encode(&api.Vote{VotePackage: json.RawMessage(pkg)}); err != nil {
				t.Errorf("encoding vote: %v", err)
			}
		default: // including the /chain/info probe apiclient.New makes and any deprecated route
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

// TestElectionMemos pages the votes list through GET /votes?electionId=&page=, stops on the page
// with a nil pagination.nextPage (never asking for the 404 page past it), and keeps only memos whose
// vote selected the open choice.
func TestElectionMemos(t *testing.T) {
	c := qt.New(t)
	electionID := []byte(strings.Repeat("e", 32))
	vid := func(s string) []byte { return []byte(strings.Repeat(s, 32)) }
	pages := [][]*api.Vote{
		{
			{VoteID: vid("a"), Memo: []byte("open-a")},
			{VoteID: vid("b")}, // no memo: never fetched
		},
		{
			{VoteID: vid("c"), Memo: []byte("closed-c")},
			{VoteID: vid("u"), Memo: []byte("unknown-u")}, // not indexed yet: dropped
		},
		{
			{VoteID: vid("d"), Memo: []byte("open-d")},
		},
	}
	packages := map[string]string{
		hex.EncodeToString(vid("a")): `{"votes":[1]}`,
		hex.EncodeToString(vid("c")): `{"votes":[0]}`,
		hex.EncodeToString(vid("d")): `{"votes":[1]}`,
	}
	selectsOpen := func(votes []int) bool { return len(votes) == 1 && votes[0] == 1 }

	var requested []int
	srv := fakeVotesNode(t, electionID, pages, packages, &requested)
	defer srv.Close()
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	a := &Account{client: client, apiEndpoint: srv.URL}

	memos, err := a.ElectionMemos(electionID, selectsOpen)
	c.Assert(err, qt.IsNil)
	c.Assert(memos, qt.DeepEquals, []string{"open-a", "open-d"})
	c.Assert(requested, qt.DeepEquals, []int{0, 1, 2})

	// an election without votes: page 0 is an empty list with a nil nextPage
	requested = nil
	empty := fakeVotesNode(t, electionID, nil, nil, &requested)
	defer empty.Close()
	a.apiEndpoint = empty.URL
	memos, err = a.ElectionMemos(electionID, selectsOpen)
	c.Assert(err, qt.IsNil)
	c.Assert(memos, qt.HasLen, 0)
	c.Assert(requested, qt.DeepEquals, []int{0})
}
