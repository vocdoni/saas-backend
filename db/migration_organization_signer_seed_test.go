package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestOrganizationSignerSeedMigration asserts migration 0029 copies the creator into the
// signerSeed field for rows missing it, leaves an existing seed alone (so a re-run after a
// creator email change cannot re-break the key), and that its down removes the field.
func TestOrganizationSignerSeedMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig, ok := migrations.AsMap()[29]
	c.Assert(ok, qt.IsTrue)
	database := testDB.DBClient.Database(testDB.database)

	const missing, empty, seeded, creatorless = "0xseedmissing", "0xseedempty", "0xseedset", "0xseednone"
	orgs := database.Collection("organizations")
	_, err := orgs.InsertMany(ctx, []any{
		bson.M{"_id": missing, "creator": "a@x.test", "nonce": "1"},
		bson.M{"_id": empty, "creator": "b@x.test", "nonce": "2", "signerSeed": ""},
		bson.M{"_id": seeded, "creator": "changed@x.test", "nonce": "3", "signerSeed": "original@x.test"},
		bson.M{"_id": creatorless, "nonce": "4"},
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() {
		_, err := orgs.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{missing, empty, seeded, creatorless}}})
		c.Assert(err, qt.IsNil)
	})

	seed := func(id string) any {
		var org bson.M
		c.Assert(orgs.FindOne(ctx, bson.M{"_id": id}).Decode(&org), qt.IsNil)
		return org["signerSeed"]
	}

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(seed(missing), qt.Equals, "a@x.test")
	c.Assert(seed(empty), qt.Equals, "b@x.test")
	c.Assert(seed(seeded), qt.Equals, "original@x.test")
	c.Assert(seed(creatorless), qt.IsNil)

	// re-running after a creator rewrite must not overwrite the stamped seed
	_, err = orgs.UpdateOne(ctx, bson.M{"_id": missing}, bson.M{"$set": bson.M{"creator": "new@x.test"}})
	c.Assert(err, qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(seed(missing), qt.Equals, "a@x.test")

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(seed(missing), qt.IsNil)
	c.Assert(seed(seeded), qt.IsNil)
}
