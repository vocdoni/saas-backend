package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
)

// fakePublishChain is a scripted chain for resolvePublishTxs: the account nonce, the chain
// height, which hashes are mined, the metadata URL of each election and the organization's
// election listing (one page).
type fakePublishChain struct {
	nonce     uint32
	nonceErr  error
	height    uint32
	heightErr error
	mined     map[string]bool
	lookupErr error
	elections map[string]string
	listing   []internal.HexBytes
}

func (f *fakePublishChain) AccountNonce(common.Address) (uint32, error) { return f.nonce, f.nonceErr }

func (f *fakePublishChain) ChainHeight() (uint32, error) { return f.height, f.heightErr }

func (f *fakePublishChain) TxMined(hash internal.HexBytes) (bool, error) {
	if f.lookupErr != nil {
		return false, f.lookupErr
	}
	return f.mined[hash.String()], nil
}

func (f *fakePublishChain) Election(id []byte) (*dvoteapi.Election, error) {
	url, ok := f.elections[internal.HexBytes(id).String()]
	if !ok {
		return nil, fmt.Errorf("election %x not found", id)
	}
	return &dvoteapi.Election{MetadataURL: url}, nil
}

func (f *fakePublishChain) OrgElectionIDs(_ common.Address, page int) ([]internal.HexBytes, error) {
	if page > 0 {
		return nil, nil
	}
	return f.listing, nil
}

// useFakePublishChain makes resolvePublishTxs ask chain until the test ends.
func useFakePublishChain(t *testing.T, chain publishTxChain) {
	t.Helper()
	testAPI.publishChainOverride = chain
	t.Cleanup(func() { testAPI.publishChainOverride = nil })
}

// newTwoQuestionDraft creates a standard organization's draft with two questions, returning the
// login token, the organization and the draft id.
func newTwoQuestionDraft(t *testing.T, password string) (string, common.Address, bson.ObjectID) {
	t.Helper()
	c := qt.New(t)
	token := testCreateUser(t, password)
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)), processesCreateEndpoint)
	return token, orgAddress, objectID(c, created.ProcessID)
}

// holdPublishTxs stores what a publish worker writes before submitting: the process flag, under a
// publish claim it then releases, and txs as the records of its first question. It returns the
// questions read back.
func holdPublishTxs(c *qt.C, oid bson.ObjectID, txs ...db.PublishTx) []db.VotingProcessQuestion {
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testDB.SetVotingProcessPublishSubmitted(oid, owner), qt.IsNil)
	questions, err := testDB.QuestionsByProcess(oid)
	c.Assert(err, qt.IsNil)
	for _, tx := range txs {
		c.Assert(testDB.AppendQuestionPublishTx(oid, questions[0].ID, tx), qt.IsNil)
	}
	c.Assert(testDB.ClearVotingProcessPublishing(oid, owner), qt.IsNil)
	questions, err = testDB.QuestionsByProcess(oid)
	c.Assert(err, qt.IsNil)
	return questions
}

// resolveStored runs resolvePublishTxs on the stored process and returns its outcome with the
// process and its first question read back.
func resolveStored(c *qt.C, oid bson.ObjectID) (bool, *db.VotingProcess, *db.VotingProcessQuestion) {
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	res, err := testAPI.resolvePublishTxs(vp, questions)
	c.Assert(err, qt.IsNil)
	vp, questions, err = testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	return res.unresolved, vp, &questions[0]
}

// randomHash returns a random 32-byte tx hash.
func randomHash() internal.HexBytes { return internal.HexBytes(internal.RandomBytes(32)) }

// resolveIDs are the hashes and election ids one resolution case plays with, fresh per case:
// election ids are unique across stored questions.
type resolveIDs struct {
	h1, h2, ours, other, served internal.HexBytes
}

// TestResolvePublishTxs covers the resolution table over a fake chain (AC3, AC9, AC10): adopt by
// the predicted id, adopt whichever of two same-nonce records mined, recover through the listing
// when the prediction is missing or points at another election (skipping one already recorded),
// dead once the nonce moved with no hash mined, dead once the chain is past the mempool TTL and
// its margin after the newest submit height with no hash mined, and unresolved otherwise.
func TestResolvePublishTxs(t *testing.T) {
	c := qt.New(t)
	const meta, otherMeta = "https://saas.test/storage/q1.json", "https://saas.test/storage/x.json"
	rec := func(hash, upstreamID internal.HexBytes) db.PublishTx {
		return db.PublishTx{Hash: hash, Nonce: 5, MetadataURL: meta, UpstreamID: upstreamID, SubmittedAt: time.Now()}
	}
	recAt := func(hash internal.HexBytes, height uint32) db.PublishTx {
		tx := rec(hash, nil)
		tx.SubmittedHeight = height
		return tx
	}
	// the height at which records submitted at 100 and 200 expire: the newest one counts
	const expiry = 200 + publishTxMempoolTTL + publishTxExpiryMargin
	type outcome int
	const (
		adopted outcome = iota
		dead
		unresolved
	)
	cases := []struct {
		name  string
		build func(id resolveIDs) ([]db.PublishTx, fakePublishChain)
		want  outcome
	}{{
		name: "adopt by the predicted id",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h1.String(): true},
				elections: map[string]string{id.ours.String(): meta},
			}
		},
		want: adopted,
	}, {
		name: "the older of two same-nonce records mined",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours), rec(id.h2, id.other)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h1.String(): true},
				elections: map[string]string{id.ours.String(): meta},
			}
		},
		want: adopted,
	}, {
		name: "the newer of two same-nonce records mined",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.other), rec(id.h2, id.ours)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h2.String(): true},
				elections: map[string]string{id.ours.String(): meta},
			}
		},
		want: adopted,
	}, {
		name: "recover through the listing: no prediction",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, nil)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h1.String(): true},
				elections: map[string]string{id.other.String(): otherMeta, id.ours.String(): meta},
				listing:   []internal.HexBytes{id.other, id.ours},
			}
		},
		want: adopted,
	}, {
		name: "recover through the listing: the prediction is another election, a recorded twin is skipped",
		// the served election carrying the same URL cannot happen once metadata carries the question id;
		// it pins the ServedUpstreamIDs filter
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.other)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h1.String(): true},
				elections: map[string]string{
					id.other.String(): otherMeta, id.ours.String(): meta, id.served.String(): meta,
				},
				listing: []internal.HexBytes{id.served, id.other, id.ours},
			}
		},
		want: adopted,
	}, {
		name: "mined but no election matches",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, nil)}, fakePublishChain{
				nonce: 6, mined: map[string]bool{id.h1.String(): true},
				elections: map[string]string{id.other.String(): otherMeta},
				listing:   []internal.HexBytes{id.other},
			}
		},
		want: unresolved,
	}, {
		name: "dead: the nonce moved and no hash mined",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours), rec(id.h2, nil)}, fakePublishChain{nonce: 6}
		},
		want: dead,
	}, {
		name: "unresolved: the nonce did not move",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours)}, fakePublishChain{nonce: 5}
		},
		want: unresolved,
	}, {
		name: "dead: the nonce did not move but the chain is past the newest record's expiry",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{recAt(id.h1, 100), recAt(id.h2, 200)}, fakePublishChain{nonce: 5, height: expiry + 1}
		},
		want: dead,
	}, {
		name: "unresolved: the chain is not past the newest record's expiry yet",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{recAt(id.h1, 100), recAt(id.h2, 200)}, fakePublishChain{nonce: 5, height: expiry}
		},
		want: unresolved,
	}, {
		name: "unresolved: the height could not be read",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{recAt(id.h1, 100)}, fakePublishChain{nonce: 5, heightErr: fmt.Errorf("status 500")}
		},
		want: unresolved,
	}, {
		name: "unresolved: a record without a submit height never expires",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{recAt(id.h1, 100), recAt(id.h2, 0)}, fakePublishChain{nonce: 5, height: 100_000}
		},
		want: unresolved,
	}, {
		name: "unresolved: a lookup failed",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours)}, fakePublishChain{nonce: 9, lookupErr: fmt.Errorf("status 500")}
		},
		want: unresolved,
	}, {
		name: "unresolved: the nonce could not be read",
		build: func(id resolveIDs) ([]db.PublishTx, fakePublishChain) {
			return []db.PublishTx{rec(id.h1, id.ours)}, fakePublishChain{nonceErr: fmt.Errorf("status 500")}
		},
		want: unresolved,
	}}
	for i, tc := range cases {
		id := resolveIDs{h1: randomHash(), h2: randomHash(), ours: randomHash(), other: randomHash(), served: randomHash()}
		txs, chain := tc.build(id)
		_, _, oid := newTwoQuestionDraft(t, fmt.Sprintf("resolvepublish%02d", i))
		questions := holdPublishTxs(c, oid, txs...)
		// the second question already records an election, which is never adopted again
		c.Assert(testDB.SetQuestionPublished(questions[1].ID, id.served, meta, db.QuestionStatusReady), qt.IsNil)
		useFakePublishChain(t, &chain)

		isUnresolved, vp, q := resolveStored(c, oid)
		comment := qt.Commentf("case %q", tc.name)
		switch tc.want {
		case adopted:
			c.Assert(isUnresolved, qt.IsFalse, comment)
			c.Assert(q.UpstreamID.String(), qt.Equals, id.ours.String(), comment)
			c.Assert(q.MetadataURL, qt.Equals, meta, comment)
			c.Assert(q.Status, qt.Equals, db.QuestionStatusReady, comment)
			c.Assert(q.PublishTxs, qt.HasLen, 0, comment)
			c.Assert(vp.HasMinedElections, qt.IsTrue, comment)
			c.Assert(vp.PublishSubmitted, qt.IsFalse, comment)
		case dead:
			c.Assert(isUnresolved, qt.IsFalse, comment)
			c.Assert(q.UpstreamID, qt.HasLen, 0, comment)
			c.Assert(q.PublishTxs, qt.HasLen, 0, comment)
			c.Assert(vp.HasMinedElections, qt.IsFalse, comment)
			c.Assert(vp.PublishSubmitted, qt.IsFalse, comment)
		default: // unresolved
			c.Assert(isUnresolved, qt.IsTrue, comment)
			c.Assert(q.UpstreamID, qt.HasLen, 0, comment)
			c.Assert(q.PublishTxs, qt.HasLen, len(txs), comment)
			c.Assert(vp.PublishSubmitted, qt.IsTrue, comment)
		}
	}
}

// TestResolvePublishTxsFlagOnly: a flag left set with no record (a crash between the flag write
// and the record appends) is cleared, and nothing is asked of the chain beyond that.
func TestResolvePublishTxsFlagOnly(t *testing.T) {
	c := qt.New(t)
	_, _, oid := newTwoQuestionDraft(t, "resolveflagonly1")
	holdPublishTxs(c, oid)
	useFakePublishChain(t, &fakePublishChain{nonceErr: fmt.Errorf("must not be asked")})

	isUnresolved, vp, _ := resolveStored(c, oid)
	c.Assert(isUnresolved, qt.IsFalse)
	c.Assert(vp.PublishSubmitted, qt.IsFalse)
}
