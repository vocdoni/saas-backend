package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMigrationPaygBilling: the billing collections and indexes exist (notably the walletLedger
// key unique per wallet, and its paymentIntentId lookup), and Up is idempotent.
func TestMigrationPaygBilling(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mig := migrationByVersion(c, 35)
	database := testDB.DBClient.Database(testDB.database)

	// already applied by RunMigrationsUp at startup; a re-run must be a no-op
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	names, err := database.ListCollectionNames(ctx, bson.M{
		"name": bson.M{"$in": bson.A{"processPayments", "wallets", "walletLedger"}},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(names, qt.HasLen, 3)

	// the ledger page sort (createdAt, then _id) is fully covered by an index
	ledger := database.Collection("walletLedger")
	specs, err := ledger.Indexes().ListSpecifications(ctx)
	c.Assert(err, qt.IsNil)
	var keys []string
	for _, spec := range specs {
		keys = append(keys, spec.KeysDocument.String())
	}
	c.Assert(keys, qt.Contains, `{"orgAddress": {"$numberInt":"1"},"createdAt": {"$numberInt":"-1"},"_id": {"$numberInt":"-1"}}`)
	// idempotency keys are unique per wallet, and only per wallet
	compound := `{"orgAddress": {"$numberInt":"1"},"idempotencyKey": {"$numberInt":"1"}}`
	c.Assert(keys, qt.Contains, compound)
	c.Assert(keys, qt.Not(qt.Contains), `{"idempotencyKey": {"$numberInt":"1"}}`)
	for _, spec := range specs {
		if spec.KeysDocument.String() == compound {
			c.Assert(spec.Unique, qt.Not(qt.IsNil))
			c.Assert(*spec.Unique, qt.IsTrue)
		}
	}

	// the same key is rejected twice in one wallet, and accepted in another
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
	entry["_id"], entry["orgAddress"] = bson.NewObjectID(), testAnotherOrgAddress
	_, err = ledger.InsertOne(ctx, entry)
	c.Assert(err, qt.IsNil)

	// a Stripe refund finds the top-up it returns by payment intent
	indexes, err := ledger.Indexes().ListSpecifications(ctx)
	c.Assert(err, qt.IsNil)
	var byIntent bool
	for _, index := range indexes {
		byIntent = byIntent || index.KeysDocument.Lookup("paymentIntentId").Type != 0
	}
	c.Assert(byIntent, qt.IsTrue)

	// the per-process fold is served by a partial (orgAddress, processId) index
	var byProcess bool
	for _, index := range indexes {
		if index.KeysDocument.String() == `{"orgAddress": {"$numberInt":"1"},"processId": {"$numberInt":"1"}}` {
			byProcess = true
		}
	}
	c.Assert(byProcess, qt.IsTrue)
	raw, err := ledger.Indexes().List(ctx)
	c.Assert(err, qt.IsNil)
	var specsRaw []bson.Raw
	c.Assert(raw.All(ctx, &specsRaw), qt.IsNil)
	var partial bool
	for _, spec := range specsRaw {
		if spec.Lookup("key", "processId").Type != 0 {
			partial, _ = spec.Lookup("partialFilterExpression", "processId", "$exists").BooleanOK()
		}
	}
	c.Assert(partial, qt.IsTrue)

	// down is a documented no-op for data-bearing collections
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
}
