package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(27, "integrator_flag", upIntegratorFlag, downIntegratorFlag)
}

// upIntegratorFlag replaces integrator limits with a plain `integrator` flag: an integrator's
// spending is bounded by its wallet alone. An organization not managed by another one becomes
// an integrator when any of these held:
//   - it carried an integratorLimits override granting managed organizations (the admin path);
//   - it subscribed to a non-default plan granting them (the portal's free integrator plan, or a
//     paid integrator plan). The default plan is left out on purpose: it grants managed orgs too,
//     so keying on it would flag every organization;
//   - it already manages organizations, or holds API keys (only integrators can mint them).
//
// Then the limits and their counters are removed from organizations and plans.
func upIntegratorFlag(ctx context.Context, database *mongo.Database) error {
	orgs := database.Collection("organizations")

	// the candidates of each of the last three rules, as lists of ids
	distinct := []struct {
		coll, field string
		filter      bson.M
		out         bson.A
	}{
		{"plans", "_id", bson.M{"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}, "default": bson.M{"$ne": true}}, nil},
		{"organizations", "managedBy", bson.M{"managedBy": bson.M{"$exists": true}}, nil},
		{"apiKeys", "orgAddress", bson.M{}, nil},
	}
	for i := range distinct {
		d := &distinct[i]
		if err := database.Collection(d.coll).Distinct(ctx, d.field, d.filter).Decode(&d.out); err != nil {
			return fmt.Errorf("failed to list integrator candidates from %s: %w", d.coll, err)
		}
	}
	filter := bson.M{
		"managedBy": bson.M{"$exists": false},
		"$or": bson.A{
			bson.M{"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}},
			bson.M{"subscription.planID": bson.M{"$in": distinct[0].out}, "subscription.active": true},
			bson.M{"_id": bson.M{"$in": distinct[1].out}},
			bson.M{"_id": bson.M{"$in": distinct[2].out}},
		},
	}
	if _, err := orgs.UpdateMany(ctx, filter, bson.M{"$set": bson.M{"integrator": true}}); err != nil {
		return fmt.Errorf("failed to flag integrators: %w", err)
	}
	if _, err := orgs.UpdateMany(ctx, bson.M{}, bson.M{"$unset": bson.M{
		"integratorLimits":          "",
		"counters.managedOrgs":      "",
		"counters.managedProcesses": "",
	}}); err != nil {
		return fmt.Errorf("failed to drop integrator limits from organizations: %w", err)
	}
	if _, err := database.Collection("plans").UpdateMany(ctx, bson.M{},
		bson.M{"$unset": bson.M{"integratorLimits": ""}}); err != nil {
		return fmt.Errorf("failed to drop integrator limits from plans: %w", err)
	}
	return nil
}

func downIntegratorFlag(context.Context, *mongo.Database) error {
	// no-op: the limits it removed are no longer enforced by anything
	return nil
}
