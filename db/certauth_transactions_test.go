package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestCertAuthTransactionStore pins the certificate sign-in store: migration 37's indexes (unique
// state, unique sparse code, TTL on expiresAt), single-use state and code consumes that bind the
// state to its own host, re-check their own deadlines and let one of many concurrent consumes win,
// and chain custody ending at the code consume.
func TestCertAuthTransactionStore(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx := context.Background()

	specs, err := testDB.certAuthTransactions.Indexes().ListSpecifications(ctx)
	c.Assert(err, qt.IsNil)
	byName := map[string]bool{}
	for _, spec := range specs {
		byName[spec.Name] = true
		switch spec.Name {
		case "state_1":
			c.Assert(spec.Unique, qt.Not(qt.IsNil))
			c.Assert(*spec.Unique, qt.IsTrue)
		case "code_1":
			c.Assert(spec.Unique, qt.Not(qt.IsNil))
			c.Assert(*spec.Unique, qt.IsTrue)
			c.Assert(spec.Sparse, qt.Not(qt.IsNil))
			c.Assert(*spec.Sparse, qt.IsTrue)
		case "expiresAt_1":
			c.Assert(spec.ExpireAfterSeconds, qt.Not(qt.IsNil))
			c.Assert(*spec.ExpireAfterSeconds, qt.Equals, int32(0))
		default:
		}
	}
	for _, name := range []string{"state_1", "code_1", "expiresAt_1"} {
		c.Assert(byName[name], qt.IsTrue, qt.Commentf("index %s", name))
	}

	now := time.Now()
	newTx := func(state, nonce string, createdAt time.Time) *CertAuthTransaction {
		tx := &CertAuthTransaction{
			State: state, Nonce: nonce, Challenge: "challenge", ProcessID: bson.NewObjectID(),
			ReturnURL: "https://app.test/processes/0123456789abcdef01234567", Purpose: "vote",
			Status: CertAuthStatusCreated, CreatedAt: createdAt, ExpiresAt: createdAt.Add(15 * time.Minute),
		}
		c.Assert(testDB.InsertCertAuthTransaction(tx), qt.IsNil)
		return tx
	}
	consume := func(state, nonce, code string, chain [][]byte) (*CertAuthTransaction, error) {
		return testDB.ConsumeCertAuthState(CertAuthStateConsume{
			State: state, Nonce: nonce, Code: code, Chain: chain, ChainPresented: len(chain) > 0,
			CreatedAfter: time.Now().Add(-15 * time.Minute), Now: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute),
		})
	}

	c.Run("state", func(c *qt.C) {
		created := newTx("state-a", "nonce-a", now)
		// a state presented on another nonce host matches nothing, and leaves it usable
		_, err := consume("state-a", "nonce-b", "code-x", nil)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		_, err = testDB.CertAuthTransactionByState("state-a", "nonce-b")
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		chain := [][]byte{[]byte("leaf"), []byte("intermediate")}
		before, err := consume("state-a", "nonce-a", "code-a", chain)
		c.Assert(err, qt.IsNil)
		c.Assert(before.ID, qt.Equals, created.ID)
		c.Assert(before.Status, qt.Equals, CertAuthStatusCreated)
		c.Assert(before.ReturnURL, qt.Equals, created.ReturnURL)
		// single use
		_, err = consume("state-a", "nonce-a", "code-a2", chain)
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		after, err := testDB.CertAuthTransactionByState("state-a", "nonce-a")
		c.Assert(err, qt.IsNil)
		c.Assert(after.Status, qt.Equals, CertAuthStatusAuthenticated)
		c.Assert(after.Code, qt.Equals, "code-a")
		c.Assert(after.Chain, qt.DeepEquals, chain)
		c.Assert(after.ChainPresented, qt.IsTrue)
		// the expiry shrank to the code TTL, which also bounds the chain's custody
		c.Assert(after.ExpiresAt.Before(now.Add(6*time.Minute)), qt.IsTrue)

		// an expired state is refused even before the TTL monitor reaps it
		newTx("state-old", "nonce-old", now.Add(-16*time.Minute))
		_, err = consume("state-old", "nonce-old", "code-old", nil)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		stale, err := testDB.CertAuthTransactionByState("state-old", "nonce-old")
		c.Assert(err, qt.IsNil)
		c.Assert(stale.Status, qt.Equals, CertAuthStatusCreated)

		// a duplicate state is refused by the unique index
		dup := &CertAuthTransaction{State: "state-a", Nonce: "nonce-z", ProcessID: bson.NewObjectID(), CreatedAt: now}
		c.Assert(testDB.InsertCertAuthTransaction(dup), qt.Not(qt.IsNil))
	})

	c.Run("concurrent state consumes", func(c *qt.C) {
		newTx("state-race", "nonce-race", now)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				if _, err := consume("state-race", "nonce-race", "code-race-"+string(rune('a'+i)), nil); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		c.Assert(wins.Load(), qt.Equals, int32(1))
	})

	c.Run("code", func(c *qt.C) {
		newTx("state-c", "nonce-c", now)
		chain := [][]byte{[]byte("leaf-c")}
		_, err := consume("state-c", "nonce-c", "code-c", chain)
		c.Assert(err, qt.IsNil)

		// an expired code is refused even before the TTL monitor reaps it
		_, err = testDB.ConsumeCertAuthCode("code-c", time.Now().Add(time.Minute))
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		before, err := testDB.ConsumeCertAuthCode("code-c", time.Now().Add(-5*time.Minute))
		c.Assert(err, qt.IsNil)
		c.Assert(before.Status, qt.Equals, CertAuthStatusAuthenticated)
		c.Assert(before.Chain, qt.DeepEquals, chain)
		// single use, and the chain is gone from storage
		_, err = testDB.ConsumeCertAuthCode("code-c", time.Now().Add(-5*time.Minute))
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		after, err := testDB.CertAuthTransactionByCode("code-c")
		c.Assert(err, qt.IsNil)
		c.Assert(after.Status, qt.Equals, CertAuthStatusConsumed)
		c.Assert(after.Chain, qt.HasLen, 0)
		raw := bson.M{}
		c.Assert(testDB.certAuthTransactions.FindOne(ctx, bson.M{"code": "code-c"}).Decode(&raw), qt.IsNil)
		_, hasChain := raw["chain"]
		c.Assert(hasChain, qt.IsFalse)

		_, err = testDB.CertAuthTransactionByCode("code-unknown")
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})

	c.Run("concurrent code consumes", func(c *qt.C) {
		newTx("state-cr", "nonce-cr", now)
		_, err := consume("state-cr", "nonce-cr", "code-cr", [][]byte{[]byte("leaf")})
		c.Assert(err, qt.IsNil)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, err := testDB.ConsumeCertAuthCode("code-cr", time.Now().Add(-5*time.Minute)); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		c.Assert(wins.Load(), qt.Equals, int32(1))
	})
}
