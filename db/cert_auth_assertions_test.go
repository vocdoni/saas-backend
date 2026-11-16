package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCertAuthAssertions(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	now := time.Now().UTC().Truncate(time.Millisecond)
	processID := bson.NewObjectID()

	store := func(code string, expiresAt time.Time) []byte {
		hash := sha256.Sum256([]byte(code))
		c.Assert(testDB.CreateCertAuthAssertion(&CertAuthAssertion{
			CodeHash: hash[:], ProcessID: processID, CensusID: "census", ParticipantID: "participant",
			CodeChallenge: "challenge", Provider: "fnmt", CertSHA256: "ab", CreatedAt: now, ExpiresAt: expiresAt,
		}), qt.IsNil)
		return hash[:]
	}

	t.Run("consumed once", func(t *testing.T) {
		c := qt.New(t)
		hash := store("code-1", now.Add(5*time.Minute))
		got, err := testDB.ConsumeCertAuthAssertion(hash, processID, now)
		c.Assert(err, qt.IsNil)
		c.Assert(got.ParticipantID, qt.Equals, "participant")
		c.Assert(got.CodeChallenge, qt.Equals, "challenge")
		c.Assert(got.UsedAt, qt.Not(qt.IsNil))
		_, err = testDB.ConsumeCertAuthAssertion(hash, processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
	})

	t.Run("expired, unknown or of another process", func(t *testing.T) {
		c := qt.New(t)
		expired := store("code-2", now.Add(-time.Second))
		_, err := testDB.ConsumeCertAuthAssertion(expired, processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
		unknown := sha256.Sum256([]byte("nope"))
		_, err = testDB.ConsumeCertAuthAssertion(unknown[:], processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
		other := store("code-3", now.Add(5*time.Minute))
		_, err = testDB.ConsumeCertAuthAssertion(other, bson.NewObjectID(), now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
	})

	t.Run("concurrent redemptions: exactly one wins", func(t *testing.T) {
		c := qt.New(t)
		hash := store("code-4", now.Add(5*time.Minute))
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for range 8 {
			wg.Go(func() {
				if _, err := testDB.ConsumeCertAuthAssertion(hash, processID, now); err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		c.Assert(wins, qt.Equals, 1)
	})

	t.Run("no document number is stored, code hash unique, TTL on expiresAt", func(t *testing.T) {
		c := qt.New(t)
		ctx := context.Background()
		var raw bson.M
		c.Assert(testDB.certAuthAssertions.FindOne(ctx, bson.M{}).Decode(&raw), qt.IsNil)
		for _, field := range []string{"nationalId", "documentType", "profile"} {
			_, present := raw[field]
			c.Assert(present, qt.IsFalse, qt.Commentf("%s stored", field))
		}
		err := testDB.CreateCertAuthAssertion(&CertAuthAssertion{
			CodeHash: raw["codeHash"].(bson.Binary).Data, ProcessID: processID, ParticipantID: "p", ExpiresAt: now,
		})
		c.Assert(err, qt.Not(qt.IsNil))
		specs, err := testDB.certAuthAssertions.Indexes().ListSpecifications(ctx)
		c.Assert(err, qt.IsNil)
		ttl := false
		for _, spec := range specs {
			if spec.Name == "expiresAt_1" && spec.ExpireAfterSeconds != nil && *spec.ExpireAfterSeconds == 0 {
				ttl = true
			}
		}
		c.Assert(ttl, qt.IsTrue)
	})
}
