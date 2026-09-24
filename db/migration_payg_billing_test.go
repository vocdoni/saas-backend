package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMigrationPaygBilling checks migration 0023: the billing collections exist with
// their indexes (the walletLedger idempotencyKey unique index is the one that matters —
// it is a money-idempotency guard, not an optimization), and Up is idempotent.
func TestMigrationPaygBilling(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mig := migrationByVersion(c, 23)
	database := testDB.DBClient.Database(testDB.database)

	// already applied by RunMigrationsUp at startup; a re-run must be a no-op
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	names, err := database.ListCollectionNames(ctx, bson.M{
		"name": bson.M{"$in": bson.A{"processPayments", "wallets", "walletLedger"}},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(names, qt.HasLen, 3)

	// the unique index on idempotencyKey rejects a duplicate audit row
	ledger := database.Collection("walletLedger")
	entry := bson.M{
		"_id": bson.NewObjectID(), "orgAddress": testOrgAddress,
		"amountCents": int64(100), "kind": "topup",
		"idempotencyKey": "cs_migration_test", "createdAt": time.Now(),
	}
	_, err = ledger.InsertOne(ctx, entry)
	c.Assert(err, qt.IsNil)
	entry["_id"] = bson.NewObjectID()
	_, err = ledger.InsertOne(ctx, entry)
	c.Assert(err, qt.Not(qt.IsNil))

	// down is a documented no-op for data-bearing collections
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
}
