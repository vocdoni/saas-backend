package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
)

// newTxReferenceNode serves the tx reference of every hash as not found until the foundOnPoll-th
// poll (0: never), and returns the client and the poll counter.
func newTxReferenceNode(c *qt.C, foundOnPoll int32) (*apiclient.HTTPclient, *atomic.Int32) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chain/transactions/reference/abcd" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if n := polls.Add(1); foundOnPoll == 0 || n < foundOnPoll {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&api.TransactionReference{Height: 7})
	}))
	c.Cleanup(srv.Close)
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	return client, &polls
}

func TestWaitTxMinedPollsUntilFound(t *testing.T) {
	c := qt.New(t)
	c.Patch(&TxPollInterval, 20*time.Millisecond)
	client, polls := newTxReferenceNode(c, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := waitTxMined(ctx, client, []byte{0xab, 0xcd})
	c.Assert(err, qt.IsNil)
	c.Assert(polls.Load(), qt.Equals, int32(3), qt.Commentf("not found twice, then found"))
}

func TestWaitTxMinedContextTimeout(t *testing.T) {
	c := qt.New(t)
	c.Patch(&TxPollInterval, 20*time.Millisecond)
	client, polls := newTxReferenceNode(c, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := waitTxMined(ctx, client, []byte{0xab, 0xcd})
	c.Assert(err, qt.ErrorIs, context.DeadlineExceeded)
	c.Assert(err, qt.ErrorMatches, `transaction abcd never mined after .*`)
	c.Assert(polls.Load() > 1, qt.IsTrue, qt.Commentf("polls: %d", polls.Load()))
}
