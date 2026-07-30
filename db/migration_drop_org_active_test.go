package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/bson"
)

// TestDropOrganizationActiveMigration checks that migration 0019 removes the `active` field left
// behind by pre-#625 writers, including the false values a partial update wrote by accident.
func TestDropOrganizationActiveMigration(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)

	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	// Seed the field the way a database written before this migration holds it: the model no
	// longer carries it, so it has to be set directly.
	_, err := testDB.organizations.UpdateOne(ctx,
		bson.M{"_id": testOrgAddress}, bson.M{"$set": bson.M{"active": false}})
	c.Assert(err, qt.IsNil)

	mig, ok := migrations.AsMap()[19]
	c.Assert(ok, qt.IsTrue)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	raw := bson.M{}
	c.Assert(testDB.organizations.FindOne(ctx, bson.M{"_id": testOrgAddress}).Decode(&raw), qt.IsNil)
	_, hasActive := raw["active"]
	c.Assert(hasActive, qt.IsFalse)

	// The rollback restores the only value the code ever wrote deliberately.
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	raw = bson.M{}
	c.Assert(testDB.organizations.FindOne(ctx, bson.M{"_id": testOrgAddress}).Decode(&raw), qt.IsNil)
	c.Assert(raw["active"], qt.IsTrue)
}
