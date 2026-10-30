package account

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
	dvotetypes "go.vocdoni.io/dvote/types"
)

// newLookupNode starts a fake node serving handler and returns an Account talking to it.
func newLookupNode(c *qt.C, handler http.HandlerFunc) *Account {
	srv := httptest.NewServer(handler)
	c.Cleanup(srv.Close)
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	return &Account{client: client}
}

// TestTxMined pins the only answers that may feed the dead rule: 200 is mined, 204 is not found,
// and anything else (a 500, a 404, a dropped connection) is an error, never "not mined". It also
// pins the endpoint: /chain/transactions/{hash}, whose 500 on an indexer read error is what makes
// the rule sound (the reference endpoint answers 204 for that error too), so a server that only
// serves the reference path must make every lookup fail.
func TestTxMined(t *testing.T) {
	c := qt.New(t)
	hash := internal.HexBytes{0xab, 0xcd}
	answers := map[string]func(http.ResponseWriter){
		"200": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&api.GenericTransactionWithInfo{TxContent: json.RawMessage(`{}`)})
		},
		"204":       func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
		"500":       func(w http.ResponseWriter) { http.Error(w, "indexer unavailable", http.StatusInternalServerError) },
		"404":       func(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) },
		"transport": func(http.ResponseWriter) { panic(http.ErrAbortHandler) },
	}
	var answer string
	a := newLookupNode(c, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chain/transactions/abcd" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		answers[answer](w)
	})

	answer = "200"
	mined, err := a.TxMined(hash)
	c.Assert(err, qt.IsNil)
	c.Assert(mined, qt.IsTrue)

	answer = "204"
	mined, err = a.TxMined(hash)
	c.Assert(err, qt.IsNil)
	c.Assert(mined, qt.IsFalse)

	for _, failing := range []string{"500", "404", "transport"} {
		answer = failing
		mined, err = a.TxMined(hash)
		c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("answer %s", failing))
		c.Assert(mined, qt.IsFalse)
	}

	// a node that only answers the reference path (200 there, nothing else) is never asked
	referenceOnly := newLookupNode(c, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chain/transactions/reference/abcd" {
			_ = json.NewEncoder(w).Encode(&api.TransactionReference{Height: 7})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	mined, err = referenceOnly.TxMined(hash)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(mined, qt.IsFalse)
}

// TestOrgElectionIDs pins the organization listing: ids newest first as the node pages them, an
// empty page past the end (the node answers {}), and an error on a non-200.
func TestOrgElectionIDs(t *testing.T) {
	c := qt.New(t)
	org := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	fail := false
	a := newLookupNode(c, func(w http.ResponseWriter, r *http.Request) {
		base := "/accounts/00000000000000000000000000000000000000aa/elections/page/"
		switch {
		case fail:
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.URL.Path == base+"0":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&api.ElectionsList{Elections: []*api.ElectionSummary{
				{ElectionID: dvotetypes.HexBytes{0x02}}, {ElectionID: dvotetypes.HexBytes{0x01}},
			}})
		case r.URL.Path == base+"1":
			_, _ = w.Write([]byte("{}"))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})

	ids, err := a.OrgElectionIDs(org, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(ids, qt.DeepEquals, []internal.HexBytes{{0x02}, {0x01}})

	ids, err = a.OrgElectionIDs(org, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(ids, qt.HasLen, 0)

	fail = true
	_, err = a.OrgElectionIDs(org, 0)
	c.Assert(err, qt.Not(qt.IsNil))
}
