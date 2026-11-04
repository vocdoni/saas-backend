package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// migrationPlans are plan documents as the previous release stored them: every plan carries
// integratorLimits (a non-pointer field without omitempty), most of them with maxManagedOrgs 0,
// and the default plan grants 10.
var migrationPlans = bson.A{
	bson.M{"_id": "prod_mig_default", "default": true, "integratorLimits": bson.M{"maxManagedOrgs": 10}},
	bson.M{"_id": "prod_mig_paid", "default": false, "integratorLimits": bson.M{"maxManagedOrgs": 0}},
	bson.M{"_id": "prod_mig_integrator", "default": false, "integratorLimits": bson.M{"maxManagedOrgs": 1}},
}

// seedMigrationPlans inserts migrationPlans and removes them when the test ends.
func seedMigrationPlans(c *qt.C) {
	ctx := context.Background()
	plans := testDB.DBClient.Database(testDB.database).Collection("plans")
	c.Cleanup(func() {
		_, _ = plans.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{
			"prod_mig_default", "prod_mig_paid", "prod_mig_integrator",
		}}})
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})
	_, err := plans.InsertMany(ctx, migrationPlans)
	c.Assert(err, qt.IsNil)
}

// seedLegacyOrg creates an organization and sets the legacy fields the model no longer carries.
func seedLegacyOrg(c *qt.C, addr common.Address, fields bson.M) {
	c.Assert(testDB.SetOrganization(&Organization{Address: addr, CreatedAt: time.Now()}), qt.IsNil)
	if len(fields) == 0 {
		return
	}
	_, err := testDB.organizations.UpdateOne(context.Background(), bson.M{"_id": addr}, bson.M{"$set": fields})
	c.Assert(err, qt.IsNil)
}

// activeOn is the legacy subscription of an organization actively subscribed to planID.
func activeOn(planID string) bson.M {
	return bson.M{"subscription.planID": planID, "subscription.active": true}
}

// TestIntegratorFlagMigration asserts migration 0034 flags exactly the organizations the previous
// release's integrator check held for — the org override first, then an active non-default plan
// granting managed organizations — and keeps the legacy limits in place.
func TestIntegratorFlagMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	seedMigrationPlans(c)

	paid, onPlan, revoked, override := common.Address{0x01}, common.Address{0x02}, common.Address{0x03}, common.Address{0x04}
	managed, zeroManaged := common.Address{0x05}, common.Address{0x06}
	onDefault, nullOverride := common.Address{0x07}, common.Address{0x08}
	inactive, keyHolder := common.Address{0x09}, common.Address{0x0a}
	seedLegacyOrg(c, paid, activeOn("prod_mig_paid"))
	seedLegacyOrg(c, onPlan, activeOn("prod_mig_integrator"))
	revokedFields := activeOn("prod_mig_integrator")
	revokedFields["integratorLimits"] = bson.M{"maxManagedOrgs": 0}
	seedLegacyOrg(c, revoked, revokedFields)
	seedLegacyOrg(c, override, bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 2}, "counters.managedOrgs": 1})
	managedFields := activeOn("prod_mig_integrator")
	managedFields["managedBy"] = onPlan
	seedLegacyOrg(c, managed, managedFields)
	// an imported database persists the zero address as managedBy
	seedLegacyOrg(c, zeroManaged, bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 2}, "managedBy": common.Address{}})
	seedLegacyOrg(c, onDefault, activeOn("prod_mig_default"))
	nullFields := activeOn("prod_mig_integrator")
	nullFields["integratorLimits"] = nil
	seedLegacyOrg(c, nullOverride, nullFields)
	seedLegacyOrg(c, inactive, bson.M{"subscription.planID": "prod_mig_integrator", "subscription.active": false})
	// the default plan let any organization mint keys: that does not flag it
	seedLegacyOrg(c, keyHolder, activeOn("prod_mig_default"))
	_, err := testDB.apiKeys.InsertOne(ctx, bson.M{"_id": bson.NewObjectID(), "orgAddress": keyHolder})
	c.Assert(err, qt.IsNil)

	mig := migrationByVersion(c, 34)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil) // idempotent

	for addr, want := range map[common.Address]bool{
		paid: false, onPlan: true, revoked: false, override: true, managed: false,
		zeroManaged: true, onDefault: false, nullOverride: true, inactive: false, keyHolder: false,
	} {
		org, err := testDB.Organization(addr)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Integrator, qt.Equals, want, qt.Commentf("org %s", addr.Hex()))
	}
	// the legacy limits stay, so a rollback still finds its integrators
	n, err := testDB.organizations.CountDocuments(ctx, bson.M{
		"_id": override, "integratorLimits.maxManagedOrgs": 2, "counters.managedOrgs": 1,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))
	n, err = database.Collection("plans").CountDocuments(ctx, bson.M{"integratorLimits": bson.M{"$exists": true}})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(len(migrationPlans)))
}

// TestIntegratorFlagMigrationStarts: neither an empty database nor one whose only organizations
// are paying customers of non-integrator plans stops the migration, and nothing gets flagged.
func TestIntegratorFlagMigrationStarts(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	mig := migrationByVersion(c, 34)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	seedMigrationPlans(c)
	customers := []common.Address{{0x21}, {0x22}, {0x23}}
	for _, addr := range customers {
		seedLegacyOrg(c, addr, activeOn("prod_mig_paid"))
	}
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	n, err := testDB.organizations.CountDocuments(ctx, bson.M{"integrator": true})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
}
