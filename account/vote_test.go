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
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
)

// TestTransientSendErr verifies the error classification used by the retry loop:
// 0/429/502/504 are transient, 500/503 are definitive, and mempoolFullMsg is not retried
// (the loop handles it separately to set maybeAccepted).
func TestTransientSendErr(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       string
		transient bool
	}{
		{"transport failure", "connection reset by peer", true},
		{"429 throttle", "API error: 429 (Too Many Requests\n)", true},
		{"502 bad gateway", "API error: 502 (bad gateway\n)", true},
		{"504 gateway timeout", "API error: 504 (gateway timeout\n)", true},
		{"500 vochain error", "API error: 500 (invalid signature\n)", false},
		{"503 mempool full", "API error: 503 (mempool full\n)", false},
		{"mempool full closed body", mempoolFullMsg, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			c.Assert(transientSendErr(fmt.Errorf("%s", tc.err)), qt.Equals, tc.transient)
		})
	}
}

// TestVoteByNullifier exercises the three answers a node can give — a vote, a 404, and any
// other status — against a fake node, since the containerized chain cannot be made to fail
// on demand. The failure case uses 500, not 503: apiclient retries 503 waiting for a block.
func TestVoteByNullifier(t *testing.T) {
	c := qt.New(t)

	known := []byte(strings.Repeat("k", 32))
	knownVote := &api.Vote{
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
		default: // including the /chain/info probe apiclient.New makes
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// the trailing slash matters: apiclient joins paths onto the base URL's path, and an
	// empty base path would yield a request line without a leading slash (a 400 from net/http)
	client, err := apiclient.New(srv.URL + "/")
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
