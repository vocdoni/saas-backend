package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// indexKeys maps a collection's index names to their key specs. The key spec is what the queries
// actually depend on — a name is derived from it, so asserting only the name would pass on an
// index whose field order or direction had drifted.
func indexKeys(c *qt.C, coll *mongo.Collection) map[string]bson.D {
	cursor, err := coll.Indexes().List(context.Background())
	c.Assert(err, qt.IsNil)
	var indexes []struct {
		Name string `bson:"name"`
		Key  bson.D `bson:"key"`
	}
	c.Assert(cursor.All(context.Background(), &indexes), qt.IsNil)
	keys := make(map[string]bson.D, len(indexes))
	for _, idx := range indexes {
		keys[idx.Name] = idx.Key
	}
	return keys
}

// TestCensusRevocationIndexesMigration asserts migration 0019 gives every cspTokens access path an
// index and the census-to-processes lookup its own, that re-running it is a no-op, and that its
// down migration drops all three.
//
// The key specs are asserted rather than the names: the compound index's field order and its
// descending createdat are what let it serve LastCSPAuth's sort and still prefix-serve the
// revocation cascade, and a name alone would not pin either.
func TestCensusRevocationIndexesMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig, ok := migrations.AsMap()[19]
	c.Assert(ok, qt.IsTrue)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
	})

	// the test database is migrated on init
	cspAuth := bson.D{{Key: "userid", Value: int32(1)}, {Key: "bundleid", Value: int32(1)}, {Key: "createdat", Value: int32(-1)}}
	c.Assert(indexKeys(c, testDB.cspTokens)["userid_1_bundleid_1_createdat_-1"], qt.DeepEquals, cspAuth)
	c.Assert(indexKeys(c, testDB.cspTokens)["bundleid_1"], qt.DeepEquals, bson.D{{Key: "bundleid", Value: int32(1)}})
	c.Assert(indexKeys(c, testDB.votingProcesses)["censusId_1"], qt.DeepEquals, bson.D{{Key: "censusId", Value: int32(1)}})

	// re-running over the already-migrated state changes nothing: an identical spec and name is a
	// server-side no-op, so a redeploy that replays the migration is safe
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(indexKeys(c, testDB.cspTokens)["userid_1_bundleid_1_createdat_-1"], qt.DeepEquals, cspAuth)

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	after := indexKeys(c, testDB.cspTokens)
	_, ok = after["userid_1_bundleid_1_createdat_-1"]
	c.Assert(ok, qt.IsFalse)
	_, ok = after["bundleid_1"]
	c.Assert(ok, qt.IsFalse)
	_, ok = indexKeys(c, testDB.votingProcesses)["censusId_1"]
	c.Assert(ok, qt.IsFalse)
}
