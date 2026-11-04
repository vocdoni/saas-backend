package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestNewWithOptionsSkipMigrations: opening a database one migration behind with SkipMigrations
// applies nothing (the migrations collection and every index stay as they were), and it ignores
// VOCDONI_MONGO_RESET_DB (no collection is dropped).
func TestNewWithOptionsSkipMigrations(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	dbName := test.RandomDatabaseName()
	migrated, err := New(mongoURI, dbName)
	c.Assert(err, qt.IsNil)
	defer migrated.Close()
	// one migration behind: forget the latest one
	migs := migrations.SortedByVersionAsc()
	latest := migs[len(migs)-1].Version
	_, err = migrated.migrations.DeleteOne(ctx, bson.M{"version": latest})
	c.Assert(err, qt.IsNil)
	database := migrated.DBClient.Database(dbName)

	snapshot := func() (names []string, records []bson.M, indexes map[string][]mongo.IndexSpecification) {
		names, err := database.ListCollectionNames(ctx, bson.M{})
		c.Assert(err, qt.IsNil)
		cur, err := database.Collection("migrations").Find(ctx, bson.M{})
		c.Assert(err, qt.IsNil)
		c.Assert(cur.All(ctx, &records), qt.IsNil)
		indexes = map[string][]mongo.IndexSpecification{}
		for _, name := range names {
			specs, err := database.Collection(name).Indexes().ListSpecifications(ctx)
			c.Assert(err, qt.IsNil)
			indexes[name] = specs
		}
		return names, records, indexes
	}
	namesBefore, recordsBefore, indexesBefore := snapshot()
	c.Assert(len(namesBefore) > 1, qt.IsTrue)

	t.Setenv("VOCDONI_MONGO_RESET_DB", "1")
	skipped, err := NewWithOptions(mongoURI, dbName, ConnectOptions{SkipMigrations: true})
	c.Assert(err, qt.IsNil)
	defer skipped.Close()

	namesAfter, recordsAfter, indexesAfter := snapshot()
	c.Assert(namesAfter, qt.ContentEquals, namesBefore)
	c.Assert(recordsAfter, qt.DeepEquals, recordsBefore)
	c.Assert(indexesAfter, qt.DeepEquals, indexesBefore)
	// the handles work: the storage reads the database it was given
	_, err = skipped.User(1)
	c.Assert(err, qt.Equals, ErrNotFound)
}
