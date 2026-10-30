package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestVerificationsPerUserTypeMigration asserts migration 0027 creates a unique (userId, type)
// index that only covers documents carrying userId, so documents written without it by replicas
// on the one-per-user scheme during a rolling deploy do not collide as (null, type), and that its
// down migration tolerates the index being gone already.
func TestVerificationsPerUserTypeMigration(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	verifications := database.Collection("verifications")
	mig := migrationByVersion(c, 27)

	// down twice: the second run finds no index to drop and must not fail
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	// documents without userId (one-per-user scheme, keyed by _id) never conflict on the index
	_, err := verifications.InsertMany(ctx, []any{
		bson.M{"_id": uint64(1001), "type": "VERIFY_ACCOUNT", "sealedCode": "a"},
		bson.M{"_id": uint64(1002), "type": "VERIFY_ACCOUNT", "sealedCode": "b"},
	})
	c.Assert(err, qt.IsNil)

	// documents with userId are unique per (userId, type)
	_, err = verifications.InsertOne(ctx, bson.M{"userId": uint64(7), "type": "VERIFY_ACCOUNT"})
	c.Assert(err, qt.IsNil)
	_, err = verifications.InsertOne(ctx, bson.M{"userId": uint64(7), "type": "PASSWORD_RESET"})
	c.Assert(err, qt.IsNil)
	_, err = verifications.InsertOne(ctx, bson.M{"userId": uint64(7), "type": "VERIFY_ACCOUNT"})
	c.Assert(mongo.IsDuplicateKeyError(err), qt.IsTrue)
}
