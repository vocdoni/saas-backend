package migrations

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(35, "integrator_flag", upIntegratorFlag, downIntegratorFlag)
}

// upIntegratorFlag replaces integrator limits with a plain `integrator` flag: an integrator's
// spending is bounded by its wallet alone. An organization not managed by another one becomes
// an integrator when either of these held:
//   - it carried an integratorLimits override granting managed organizations (the admin path);
//   - it had an active subscription to a non-default plan granting them (the portal's free
//     integrator plan, or a paid integrator plan). The default plan is left out on purpose: it
//     grants managed orgs too, so keying on it would flag every organization.
//
// Holding API keys or managing organizations is not enough on its own: the default plan let any
// organization do both, and flagging those would lock them out of creating processes.
//
// An organization is unmanaged when managedBy is missing, null or the zero address (an exported
// database restored through ImportData persists the latter). The migration fails closed: it
// errors out, before dropping the limits, if unmanaged orgs carried them yet none got flagged.
//
// Then the limits and their counters are removed from organizations and plans.
func upIntegratorFlag(ctx context.Context, database *mongo.Database) error {
	orgs := database.Collection("organizations")
	// field missing, null, or the zero address
	unmanaged := bson.M{"$in": bson.A{nil, common.Address{}}}

	var integratorPlans bson.A
	if err := database.Collection("plans").Distinct(ctx, "_id", bson.M{
		"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}, "default": bson.M{"$ne": true},
	}).Decode(&integratorPlans); err != nil {
		return fmt.Errorf("failed to list integrator plans: %w", err)
	}
	res, err := orgs.UpdateMany(ctx, bson.M{
		"managedBy": unmanaged,
		"$or": bson.A{
			bson.M{"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}},
			bson.M{"subscription.planID": bson.M{"$in": integratorPlans}, "subscription.active": true},
		},
	}, bson.M{"$set": bson.M{"integrator": true}})
	if err != nil {
		return fmt.Errorf("failed to flag integrators: %w", err)
	}
	if res.MatchedCount == 0 {
		withLimits, err := orgs.CountDocuments(ctx, bson.M{
			"managedBy": unmanaged, "integratorLimits.maxManagedOrgs": bson.M{"$gt": 0},
		})
		if err != nil {
			return fmt.Errorf("failed to count organizations with integrator limits: %w", err)
		}
		if withLimits > 0 {
			return fmt.Errorf("no integrator flagged although %d organizations carry integrator limits", withLimits)
		}
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
