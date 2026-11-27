package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// cspTokensTTLIndex is the TTL index migration 41 creates on cspTokens.
const cspTokensTTLIndex = "csp_tokens_unverified_ttl"

// TestCSPCodeExpiryMigration checks that migration 41 creates a TTL index on createdat that holds
// unverified codes only, that re-running it is a no-op, and that its down migration drops it.
func TestCSPCodeExpiryMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig := migrationByVersion(c, 41)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	now := time.Now().Truncate(time.Millisecond)
	pending := internal.HexBytes(uuid.New().String())
	verified := internal.HexBytes(uuid.New().String())
	doc := func(token internal.HexBytes) bson.M {
		return bson.M{
			"_id": internal.HexBytes(internal.RandomBytes(16)), "userid": testUserID, "bundleid": testCSPBundleID,
			"createdat": now, "attempts": 0, "resends": 0,
			"tokens": bson.A{bson.M{"id": token, "createdat": now}},
		}
	}
	pendingDoc := doc(pending)
	pendingDoc["secret"] = "secret"
	verifiedDoc := doc(verified)
	verifiedDoc["verifiedby"] = verified
	verifiedDoc["verifiedat"] = now
	_, err := testDB.cspTokens.InsertMany(ctx, []any{pendingDoc, verifiedDoc})
	c.Assert(err, qt.IsNil)

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	// re-running it changes nothing
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	type indexSpec struct {
		Name               string `bson:"name"`
		Key                bson.D `bson:"key"`
		ExpireAfterSeconds *int32 `bson:"expireAfterSeconds"`
		PartialFilter      bson.D `bson:"partialFilterExpression"`
	}
	ttlIndex := func() (indexSpec, bool) {
		cursor, err := testDB.cspTokens.Indexes().List(ctx)
		c.Assert(err, qt.IsNil)
		var indexes []indexSpec
		c.Assert(cursor.All(ctx, &indexes), qt.IsNil)
		for _, idx := range indexes {
			if idx.Name == cspTokensTTLIndex {
				return idx, true
			}
		}
		return indexSpec{}, false
	}
	idx, ok := ttlIndex()
	c.Assert(ok, qt.IsTrue)
	c.Assert(idx.Key, qt.DeepEquals, bson.D{{Key: "createdat", Value: int32(1)}})
	c.Assert(idx.ExpireAfterSeconds, qt.Not(qt.IsNil))
	c.Assert(*idx.ExpireAfterSeconds, qt.Equals, int32((25 * time.Hour).Seconds()))
	c.Assert(idx.PartialFilter, qt.DeepEquals, bson.D{{Key: "verifiedby", Value: nil}})

	// the index holds the unverified code only, so the TTL monitor never deletes a verified one
	var inIndex []CSPAuth
	cursor, err := testDB.cspTokens.Find(ctx, bson.M{}, options.Find().SetHint(cspTokensTTLIndex))
	c.Assert(err, qt.IsNil)
	c.Assert(cursor.All(ctx, &inIndex), qt.IsNil)
	c.Assert(inIndex, qt.HasLen, 1)
	c.Assert(inIndex[0].Secret, qt.Equals, "secret")

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	_, ok = ttlIndex()
	c.Assert(ok, qt.IsFalse)
	// dropping an index already gone must not fail
	c.Assert(mig.Down(ctx, database), qt.IsNil)
}
