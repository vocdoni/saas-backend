package account

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/chainclient"
)

// TestVoteByNullifier exercises the three answers a node can give — a vote, a 404, and any
// other status — against a fake node, since the containerized chain cannot be made to fail
// on demand. The failure case uses 500, not 503: the client retries 503 waiting for a block.
func TestVoteByNullifier(t *testing.T) {
	c := qt.New(t)

	known := []byte(strings.Repeat("k", 32))
	knownVote := &chainclient.Vote{
		ElectionID:  []byte(strings.Repeat("e", 32)),
		TxHash:      []byte(strings.Repeat("t", 32)),
		BlockHeight: 42,
	}
	broken := []byte(strings.Repeat("b", 32))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/votes/" + hex.EncodeToString(known):
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(knownVote); err != nil {
				t.Errorf("encoding vote: %v", err)
			}
		case "/votes/" + hex.EncodeToString(broken):
			http.Error(w, "boom", http.StatusInternalServerError)
		default: // including the /chain/info probe chainclient.New makes
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := chainclient.New(srv.URL)
	c.Assert(err, qt.IsNil)
	a := &Account{client: client}

	vote, err := a.VoteByNullifier(known)
	c.Assert(err, qt.IsNil)
	c.Assert([]byte(vote.ElectionID), qt.DeepEquals, []byte(knownVote.ElectionID))
	c.Assert([]byte(vote.TxHash), qt.DeepEquals, []byte(knownVote.TxHash))
	c.Assert(vote.BlockHeight, qt.Equals, knownVote.BlockHeight)

	_, err = a.VoteByNullifier([]byte(strings.Repeat("u", 32)))
	c.Assert(err, qt.ErrorIs, ErrVoteNotFound)

	_, err = a.VoteByNullifier(broken)
	c.Assert(err, qt.Not(qt.ErrorIs), ErrVoteNotFound, qt.Commentf("a node failure must not read as not-found"))
	c.Assert(err, qt.ErrorMatches, fmt.Sprintf("(?s)could not fetch vote %x: status 500.*", broken))
}
