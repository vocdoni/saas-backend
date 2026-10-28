package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestIntegratorFlagMigration asserts migration 0035 flags as integrators exactly the
// organizations that were one under integrator limits (override, non-default integrator plan),
// never one only on the default plan — even holding an API key or managing an org — nor a
// managed one, and strips the limits and their counters.
func TestIntegratorFlagMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	plans := database.Collection("plans")
	c.Cleanup(func() {
		_, _ = plans.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{"prod_mig_default", "prod_mig_integrator"}}})
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	_, err := plans.InsertMany(ctx, bson.A{
		bson.M{"_id": "prod_mig_default", "default": true, "integratorLimits": bson.M{"maxManagedOrgs": 10}},
		bson.M{"_id": "prod_mig_integrator", "default": false, "integratorLimits": bson.M{"maxManagedOrgs": 1}},
	})
	c.Assert(err, qt.IsNil)

	override, onPlan, managing, keyHolder := common.Address{0x01}, common.Address{0x02}, common.Address{0x03}, common.Address{0x04}
	onDefault, managed, zeroManaged := common.Address{0x05}, common.Address{0x06}, common.Address{0x07}
	for _, addr := range []common.Address{override, onPlan, managing, keyHolder, onDefault, zeroManaged} {
		c.Assert(testDB.SetOrganization(&Organization{Address: addr, CreatedAt: time.Now()}), qt.IsNil)
	}
	c.Assert(testDB.SetOrganization(&Organization{Address: managed, ManagedBy: managing, CreatedAt: time.Now()}), qt.IsNil)
	// the legacy fields the model no longer carries are seeded directly
	set := func(addr common.Address, fields bson.M) {
		_, err := testDB.organizations.UpdateOne(ctx, bson.M{"_id": addr}, bson.M{"$set": fields})
		c.Assert(err, qt.IsNil)
	}
	set(override, bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 2}, "counters.managedOrgs": 1})
	// an imported database persists the zero address as managedBy
	set(zeroManaged, bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 2}, "managedBy": common.Address{}})
	set(onPlan, bson.M{"subscription.planID": "prod_mig_integrator", "subscription.active": true})
	set(onDefault, bson.M{"subscription.planID": "prod_mig_default", "subscription.active": true})
	// the default plan let any organization mint keys and create managed orgs: neither flags it
	set(managing, bson.M{"subscription.planID": "prod_mig_default", "subscription.active": true})
	set(keyHolder, bson.M{"subscription.planID": "prod_mig_default", "subscription.active": true})
	set(managed, bson.M{"subscription.planID": "prod_mig_integrator", "subscription.active": true})
	_, err = testDB.apiKeys.InsertOne(ctx, bson.M{"_id": bson.NewObjectID(), "orgAddress": keyHolder})
	c.Assert(err, qt.IsNil)

	mig := migrationByVersion(c, 35)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil) // idempotent

	for addr, want := range map[common.Address]bool{
		override: true, onPlan: true, managing: false, keyHolder: false, onDefault: false, managed: false, zeroManaged: true,
	} {
		org, err := testDB.Organization(addr)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Integrator, qt.Equals, want, qt.Commentf("org %s", addr.Hex()))
	}
	n, err := testDB.organizations.CountDocuments(ctx, bson.M{"$or": bson.A{
		bson.M{"integratorLimits": bson.M{"$exists": true}},
		bson.M{"counters.managedOrgs": bson.M{"$exists": true}},
	}})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
	n, err = plans.CountDocuments(ctx, bson.M{"integratorLimits": bson.M{"$exists": true}})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
}

// TestIntegratorFlagMigrationFailsClosed: when unmanaged organizations carry integrator limits
// but the flag rules match none of them, the migration errors out and keeps the limits.
func TestIntegratorFlagMigrationFailsClosed(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	addr := common.Address{0x11}
	c.Assert(testDB.SetOrganization(&Organization{Address: addr, CreatedAt: time.Now()}), qt.IsNil)
	_, err := testDB.organizations.UpdateOne(ctx, bson.M{"_id": addr},
		bson.M{"$set": bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 0}}})
	c.Assert(err, qt.IsNil)

	mig := migrationByVersion(c, 35)
	c.Assert(mig.Up(ctx, testDB.DBClient.Database(testDB.database)), qt.ErrorMatches, "no integrator flagged.*")
	n, err := testDB.organizations.CountDocuments(ctx, bson.M{"_id": addr, "integratorLimits": bson.M{"$exists": true}})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))
}
