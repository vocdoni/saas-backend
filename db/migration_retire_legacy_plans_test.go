package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// legacyOrgDoc is an organization as the subscription-model release stored it.
func legacyOrgDoc(addr common.Address, planID string, extra bson.M) bson.M {
	doc := bson.M{
		"_id":     addr,
		"creator": "legacy@example.com",
		"subscription": bson.M{
			"planID": planID, "stripeSubscriptionID": "sub_" + addr.Hex()[2:8], "billingPeriod": "year",
			"startDate": time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), "renewalDate": time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			"lastPaymentDate": time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC), "active": true, "email": "billing@example.com",
		},
		"counters": bson.M{
			"sentSMS": 3, "sentEmails": 40, "sentVotes": 500, "subOrgs": 1, "users": 4,
			"processes": 7, "managedOrgs": 2, "managedProcesses": 5,
		},
		"integratorLimits": nil,
		"tokensPurchased":  int64(1000),
		"tokensRemaining":  int64(250),
	}
	for k, v := range extra {
		doc[k] = v
	}
	return doc
}

// rawDoc reads one document as stored.
func rawDoc(c *qt.C, collection string, id any) bson.Raw {
	var doc bson.Raw
	c.Assert(testDB.DBClient.Database(testDB.database).Collection(collection).
		FindOne(context.Background(), bson.M{"_id": id}).Decode(&doc), qt.IsNil)
	return doc
}

// TestRetireLegacyPlansMigration asserts migration 0036 archives every organization's legacy
// billing data once (with the plan it was subscribed to), strips it from the organization keeping
// the sent counters, drops plans, and converges when re-run, also after a run cut between the
// archive and the unset of an organization.
func TestRetireLegacyPlansMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	archive := database.Collection("legacySubscriptions")
	seedMigrationPlans(c)
	c.Cleanup(func() { _, _ = archive.DeleteMany(ctx, bson.M{}) })

	paid, managed, onDefault := common.Address{0x31}, common.Address{0x32}, common.Address{0x33}
	noPlanDoc, noSubscription, halfDone := common.Address{0x34}, common.Address{0x35}, common.Address{0x36}
	_, err := testDB.organizations.InsertMany(ctx, []any{
		legacyOrgDoc(paid, "prod_mig_paid", bson.M{"integratorLimits": bson.M{"maxManagedOrgs": 3}}),
		legacyOrgDoc(managed, "prod_mig_default", bson.M{"managedBy": paid}),
		legacyOrgDoc(onDefault, "prod_mig_default", nil),
		legacyOrgDoc(noPlanDoc, "prod_mig_gone", nil),
		bson.M{"_id": noSubscription, "counters": bson.M{"sentSMS": 1, "sentEmails": 2, "sentVotes": 3, "users": 1}},
		legacyOrgDoc(halfDone, "prod_mig_paid", nil),
	})
	c.Assert(err, qt.IsNil)
	// a run cut after archiving halfDone: its archive is kept, whatever the organization holds now
	firstArchive := bson.M{"_id": halfDone, "subscription": bson.M{"planID": "prod_mig_integrator"}, "plan": nil}
	_, err = archive.InsertOne(ctx, firstArchive)
	c.Assert(err, qt.IsNil)

	orgs := []common.Address{paid, managed, onDefault, noPlanDoc, noSubscription}
	before := map[common.Address]bson.Raw{}
	for _, addr := range orgs {
		before[addr] = rawDoc(c, "organizations", addr)
	}
	plans := map[string]bson.Raw{
		"prod_mig_paid":    rawDoc(c, "plans", "prod_mig_paid"),
		"prod_mig_default": rawDoc(c, "plans", "prod_mig_default"),
	}

	mig := migrationByVersion(c, 36)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	assertRetired := func(addr common.Address) {
		org := rawDoc(c, "organizations", addr)
		for _, field := range []string{"subscription", "integratorLimits", "tokensPurchased", "tokensRemaining"} {
			_, err := org.LookupErr(field)
			c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("org %s keeps %s", addr.Hex(), field))
		}
		var counters struct {
			Counters map[string]int `bson:"counters"`
		}
		c.Assert(bson.Unmarshal(org, &counters), qt.IsNil)
		c.Assert(counters.Counters, qt.HasLen, 3, qt.Commentf("org %s counters %v", addr.Hex(), counters.Counters))
		for _, kept := range []string{"sentSMS", "sentEmails", "sentVotes"} {
			_, ok := counters.Counters[kept]
			c.Assert(ok, qt.IsTrue)
		}
	}
	for _, addr := range append(orgs, halfDone) {
		assertRetired(addr)
	}
	c.Assert(rawDoc(c, "organizations", managed).Lookup("managedBy").Equal(before[managed].Lookup("managedBy")), qt.IsTrue)
	c.Assert(rawDoc(c, "organizations", paid).Lookup("creator").StringValue(), qt.Equals, "legacy@example.com")

	// the archive holds the original fields and the plan document, null when there is none
	wantPlan := map[common.Address]bson.Raw{
		paid: plans["prod_mig_paid"], managed: plans["prod_mig_default"], onDefault: plans["prod_mig_default"],
	}
	archived := map[common.Address]bson.Raw{}
	for _, addr := range orgs {
		row := rawDoc(c, "legacySubscriptions", addr)
		archived[addr] = row
		for _, field := range []string{
			"subscription", "counters", "integratorLimits", "tokensPurchased", "tokensRemaining", "managedBy",
		} {
			// a field the organization lacked is absent from the archive too: two zero values
			want, got := before[addr].Lookup(field), row.Lookup(field)
			if field == "subscription" && want.Type == bson.TypeEmbeddedDocument {
				// the billing email is not archived, every other field is as it was
				wantElems, err := want.Document().Elements()
				c.Assert(err, qt.IsNil)
				gotElems, err := got.Document().Elements()
				c.Assert(err, qt.IsNil)
				c.Assert(wantElems, qt.HasLen, len(gotElems)+1)
				_, err = got.Document().LookupErr("email")
				c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("org %s archives the billing email", addr.Hex()))
				for _, elem := range gotElems {
					c.Assert(elem.Value().Equal(want.Document().Lookup(elem.Key())), qt.IsTrue)
				}
				continue
			}
			c.Assert(got.Equal(want), qt.IsTrue, qt.Commentf("org %s field %s", addr.Hex(), field))
		}
		if plan, ok := wantPlan[addr]; ok {
			c.Assert(row.Lookup("plan").Document(), qt.DeepEquals, plan)
		} else {
			c.Assert(row.Lookup("plan").Type, qt.Equals, bson.TypeNull, qt.Commentf("org %s", addr.Hex()))
		}
		c.Assert(row.Lookup("archivedAt").Type, qt.Equals, bson.TypeDateTime)
	}
	half := rawDoc(c, "legacySubscriptions", halfDone)
	c.Assert(half.Lookup("subscription", "planID").StringValue(), qt.Equals, "prod_mig_integrator")
	_, err = half.LookupErr("archivedAt")
	c.Assert(err, qt.Not(qt.IsNil))

	names, err := database.ListCollectionNames(ctx, bson.M{"name": bson.M{"$in": bson.A{"plans", "subscriptions"}}})
	c.Assert(err, qt.IsNil)
	c.Assert(names, qt.HasLen, 0) // the empty subscriptions collection is dropped too

	// a re-run changes nothing: the archive keeps the first run's values
	after := map[common.Address]bson.Raw{}
	for _, addr := range orgs {
		after[addr] = rawDoc(c, "organizations", addr)
	}
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	for _, addr := range orgs {
		c.Assert(rawDoc(c, "organizations", addr), qt.DeepEquals, after[addr])
		c.Assert(rawDoc(c, "legacySubscriptions", addr), qt.DeepEquals, archived[addr])
	}
	n, err := archive.CountDocuments(ctx, bson.M{})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(len(orgs)+1))
}

// TestRetireLegacyPlansMigrationEmpty: a database without legacy organizations or plans (a fresh
// one) migrates without error and archives nothing.
func TestRetireLegacyPlansMigrationEmpty(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	_, err := testDB.organizations.InsertOne(ctx, bson.M{"_id": common.Address{0x41}, "counters": bson.M{"sentSMS": 1}})
	c.Assert(err, qt.IsNil)

	// a subscriptions collection holding data is left in place
	_, err = database.Collection("subscriptions").InsertOne(ctx, bson.M{"note": "kept"},
		options.InsertOne().SetBypassDocumentValidation(true))
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = database.Collection("subscriptions").Drop(ctx) })

	mig := migrationByVersion(c, 36)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	n, err := database.Collection("legacySubscriptions").CountDocuments(ctx, bson.M{})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
	kept, err := database.Collection("subscriptions").CountDocuments(ctx, bson.M{})
	c.Assert(err, qt.IsNil)
	c.Assert(kept, qt.Equals, int64(1))
}
