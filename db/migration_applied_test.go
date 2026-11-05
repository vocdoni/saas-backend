package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMigrationApplied pins the ledger check the certificate census publish guard relies on: false
// for a migration the ledger lacks, true once RunMigrationsUp applied it.
func TestMigrationApplied(t *testing.T) {
	c := qt.New(t)
	// a database of its own: the test rewrites its ledger
	store, err := New(mongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	defer store.Close()
	version := migrations.NationalIDNormalizationVersion

	applied, err := store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsTrue)

	// a ledger that never ran it (nor any later migration, so RunMigrationsUp runs it again)
	_, err = store.migrations.DeleteMany(context.Background(), bson.M{"version": bson.M{"$gte": version}})
	c.Assert(err, qt.IsNil)
	applied, err = store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsFalse)

	c.Assert(store.RunMigrationsUp(), qt.IsNil)
	applied, err = store.MigrationApplied(version)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsTrue)

	applied, err = store.MigrationApplied(9999)
	c.Assert(err, qt.IsNil)
	c.Assert(applied, qt.IsFalse)
}
