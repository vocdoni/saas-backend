package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestCSPSendBackoffMigration checks that migration 42 creates the TTL index on the send backoff
// expiry and that its down migration drops it, tolerating it being already gone.
func TestCSPSendBackoffMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig := migrationByVersion(c, 42)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
	})

	type indexSpec struct {
		Name               string `bson:"name"`
		Key                bson.D `bson:"key"`
		ExpireAfterSeconds *int32 `bson:"expireAfterSeconds"`
	}
	ttlIndex := func() (indexSpec, bool) {
		cursor, err := testDB.cspSendBackoff.Indexes().List(ctx)
		c.Assert(err, qt.IsNil)
		var indexes []indexSpec
		c.Assert(cursor.All(ctx, &indexes), qt.IsNil)
		for _, idx := range indexes {
			if idx.Name == "csp_send_backoff_expires_at" {
				return idx, true
			}
		}
		return indexSpec{}, false
	}

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	idx, ok := ttlIndex()
	c.Assert(ok, qt.IsTrue)
	c.Assert(idx.Key, qt.DeepEquals, bson.D{{Key: "expiresat", Value: int32(1)}})
	c.Assert(idx.ExpireAfterSeconds, qt.Not(qt.IsNil))
	c.Assert(*idx.ExpireAfterSeconds, qt.Equals, int32(0))
	// re-running it is harmless
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	_, ok = ttlIndex()
	c.Assert(ok, qt.IsFalse)
	c.Assert(mig.Down(ctx, database), qt.IsNil)
}
