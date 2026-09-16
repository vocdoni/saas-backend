package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestOrganizationDefaultLangMigration asserts migration 0021 stamps the default language on both
// shapes an organization predating the field can have, leaves a deliberate one alone, and that its
// down removes the field. The missing-key shape is the one real deployments hold, and a plain
// {"defaultLang": ""} equality would not match it.
func TestOrganizationDefaultLangMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig, ok := migrations.AsMap()[21]
	c.Assert(ok, qt.IsTrue)
	database := testDB.DBClient.Database(testDB.database)

	const missing, empty, chosen = "0xmigmissing", "0xmigempty", "0xmigchosen"
	orgs := database.Collection("organizations")
	_, err := orgs.InsertMany(ctx, []any{
		bson.M{"_id": missing},
		bson.M{"_id": empty, "defaultLang": ""},
		bson.M{"_id": chosen, "defaultLang": "ca"},
	})
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() {
		_, err := orgs.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{missing, empty, chosen}}})
		c.Assert(err, qt.IsNil)
	})

	lang := func(id string) any {
		var org bson.M
		c.Assert(orgs.FindOne(ctx, bson.M{"_id": id}).Decode(&org), qt.IsNil)
		return org["defaultLang"]
	}

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(lang(missing), qt.Equals, "en")
	c.Assert(lang(empty), qt.Equals, "en")
	c.Assert(lang(chosen), qt.Equals, "ca")

	// re-running matches nothing, so a redeploy cannot overwrite a language chosen in between
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(lang(chosen), qt.Equals, "ca")

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	c.Assert(lang(missing), qt.IsNil)
	c.Assert(lang(chosen), qt.IsNil)

	// restore the migrated state for the rest of the suite
	c.Assert(mig.Up(ctx, database), qt.IsNil)
}
