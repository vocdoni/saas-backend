package migrations

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(34, "integrator_flag", upIntegratorFlag, downIntegratorFlag)
}

// upIntegratorFlag replaces integrator limits with a plain `integrator` flag: an integrator's
// spending is bounded by its wallet alone. It flags every organization not managed by another one
// for which the integrator check this replaces held:
//   - an integratorLimits override decides on its own: maxManagedOrgs > 0 flags it, and an
//     override of 0 revokes it even on an integrator plan;
//   - with no override (missing or null), an active subscription to a plan granting managed
//     organizations flags it.
//
// The one intentional difference is the default plan: it grants managed organizations too, so the
// old check held for every organization on it, and keying on it would flag them all and lock them
// out of creating processes. Holding API keys or managing organizations is not enough either,
// since the default plan let any organization do both.
//
// An organization is unmanaged when managedBy is missing, null or the zero address (an exported
// database restored through ImportData persists the latter).
//
// The limits and their counters are left in place: nothing reads them any more, and keeping them
// lets a rollback to the previous release find its integrators again.
func upIntegratorFlag(ctx context.Context, database *mongo.Database) error {
	var integratorPlans bson.A
	if err := database.Collection("plans").Distinct(ctx, "_id", bson.M{
		"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}, "default": bson.M{"$ne": true},
	}).Decode(&integratorPlans); err != nil {
		return fmt.Errorf("failed to list integrator plans: %w", err)
	}
	if _, err := database.Collection("organizations").UpdateMany(ctx, bson.M{
		// field missing, null, or the zero address
		"managedBy": bson.M{"$in": bson.A{nil, common.Address{}}},
		"$or": bson.A{
			bson.M{"integratorLimits.maxManagedOrgs": bson.M{"$gt": 0}},
			// null matches a missing override too
			bson.M{
				"integratorLimits":    nil,
				"subscription.planID": bson.M{"$in": integratorPlans},
				"subscription.active": true,
			},
		},
	}, bson.M{"$set": bson.M{"integrator": true}}); err != nil {
		return fmt.Errorf("failed to flag integrators: %w", err)
	}
	return nil
}

func downIntegratorFlag(context.Context, *mongo.Database) error {
	// no-op: the up migration only adds the flag, which the previous release ignores
	return nil
}
