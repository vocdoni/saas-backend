package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/vochain/state"
)

// fakeLookup is a VoteOnChainLookup over a set of on-chain nullifiers. It counts its calls and
// fails for the nullifiers in failing.
type fakeLookup struct {
	mu      sync.Mutex
	onChain map[string]bool
	failing map[string]bool
	calls   int
}

func (f *fakeLookup) lookup(_ context.Context, nullifier []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failing[string(nullifier)] {
		return false, fmt.Errorf("node unreachable")
	}
	return f.onChain[string(nullifier)], nil
}

func (f *fakeLookup) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// checkFixture is a voting process with four published questions and one verified voter who:
// did not sign q0, signed q1 with an address that has no vote on chain, signed q2 with one that
// does, and signed q3 in a blind census (no address).
type checkFixture struct {
	pid        string
	token      internal.HexBytes
	userID     internal.HexBytes
	upstream   [4]internal.HexBytes
	nullifier1 []byte
	nullifier2 []byte
}

func newCheckFixture(c *qt.C) *checkFixture {
	org := common.Address{0xc4, 0xec, byte(time.Now().UnixNano())}
	c.Assert(testDB.SetOrganization(&db.Organization{Address: org, CreatedAt: time.Now()}), qt.IsNil)
	oid, err := testDB.SetVotingProcess(&db.VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	f := &checkFixture{
		pid:    oid.Hex(),
		token:  internal.HexBytes(internal.RandomBytes(16)),
		userID: internal.HexBytesFromString(bson.NewObjectID().Hex()),
	}
	for i := range f.upstream {
		f.upstream[i] = internal.HexBytes(internal.RandomBytes(32))
		_, err := testDB.SetQuestion(&db.VotingProcessQuestion{
			ProcessID: oid, OrgAddress: org, Order: i, UpstreamID: f.upstream[i],
		})
		c.Assert(err, qt.IsNil)
	}
	c.Assert(testDB.SetCSPAuth(f.token, f.userID, internal.HexBytes(oid[:]), ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(f.token), qt.IsNil)

	notVoted := common.Address{0x01}
	voted := common.Address{0x02}
	c.Assert(testDB.ConsumeCSPProcess(f.token, f.upstream[1], notVoted.Bytes()), qt.IsNil)
	c.Assert(testDB.ConsumeCSPProcess(f.token, f.upstream[2], voted.Bytes()), qt.IsNil)
	f.nullifier1 = state.GenerateNullifier(notVoted, f.upstream[1])
	f.nullifier2 = state.GenerateNullifier(voted, f.upstream[2])

	_, _, err = testDB.ArmCSPProcessBlind(f.token, f.upstream[3],
		internal.HexBytes{0x0a}, internal.HexBytes{0x0b}, internal.HexBytes{0x01})
	c.Assert(err, qt.IsNil)
	_, err = testDB.ClaimCSPProcessBlind(f.userID, f.upstream[3], f.token)
	c.Assert(err, qt.IsNil)
	return f
}

// check calls POST /processes/{pid}/check through a router, as the API mounts it.
func (f *checkFixture) check(c *qt.C, lookup VoteOnChainLookup) *httptest.ResponseRecorder {
	h := New(&csp.CSP{Storage: testDB}, testDB, lookup)
	router := chi.NewRouter()
	router.Post("/processes/{processId}/check", h.ProcessCheckHandler)
	body, err := json.Marshal(&CheckMembershipRequest{AuthToken: f.token})
	c.Assert(err, qt.IsNil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/processes/"+f.pid+"/check", bytes.NewReader(body)))
	return rec
}

// AC5: hasSigned is the CSP signature, hasVoted the vote on chain; a vote found is cached, a
// blind row never reaches the chain, and a failed lookup fails the call.
func TestProcessCheckVoteStatus(t *testing.T) {
	c := qt.New(t)
	f := newCheckFixture(c)
	lookup := &fakeLookup{onChain: map[string]bool{string(f.nullifier2): true}}

	rec := f.check(c, lookup.lookup)
	c.Assert(rec.Code, qt.Equals, http.StatusOK, qt.Commentf("body: %s", rec.Body))
	var resp ProcessCheckResponse
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &resp), qt.IsNil)
	c.Assert(resp.Questions, qt.HasLen, 4)
	type status struct{ signed, voted bool }
	want := []status{{false, false}, {true, false}, {true, true}, {true, false}}
	for i, q := range resp.Questions {
		c.Assert(q.UpstreamID, qt.DeepEquals, f.upstream[i])
		c.Assert(status{q.HasSigned, q.HasVoted}, qt.Equals, want[i], qt.Commentf("question %d", i))
	}
	// only the two signed, non-blind rows were looked up
	c.Assert(lookup.callCount(), qt.Equals, 2)
	row, err := testDB.CSPProcessByUserAndProcess(f.userID, f.upstream[2])
	c.Assert(err, qt.IsNil)
	c.Assert(row.VotedOnChain, qt.IsTrue)
	row, err = testDB.CSPProcessByUserAndProcess(f.userID, f.upstream[1])
	c.Assert(err, qt.IsNil)
	c.Assert(row.VotedOnChain, qt.IsFalse)

	// the cached row is not looked up again; the not-yet-voted one is
	rec = f.check(c, lookup.lookup)
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &resp), qt.IsNil)
	c.Assert(resp.Questions[2].HasVoted, qt.IsTrue)
	c.Assert(lookup.callCount(), qt.Equals, 3)

	// AC5b: a lookup that fails is a 500, never a hasVoted=false
	failing := &fakeLookup{failing: map[string]bool{string(f.nullifier1): true}}
	rec = f.check(c, failing.lookup)
	c.Assert(rec.Code, qt.Equals, http.StatusInternalServerError)
	var apiErr struct {
		Code int `json:"code"`
	}
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &apiErr), qt.IsNil)
	c.Assert(apiErr.Code, qt.Equals, errors.ErrVochainRequestFailed.Code)
}
