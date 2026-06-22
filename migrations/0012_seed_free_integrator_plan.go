package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(12, "seed_free_integrator_plan", upSeedFreeIntegratorPlan, downSeedFreeIntegratorPlan)
}

// freeIntegratorPlanID is a fixed, high sentinel id so it never collides with the
// (small, config-index based) ids that the Stripe plan sync assigns. nextPlanID is
// max(_id)+1, so Stripe-synced plans keep using their explicit small ids via the update
// path; only future auto-assigned ids would start past this sentinel.
const freeIntegratorPlanID uint64 = 9001

// freeIntegratorPlanDoc mirrors the bson shape of the relevant db.Plan fields. It is kept
// local to the migration (rather than importing db) so historical migrations stay pinned to
// the schema as it was when written.
type freeIntegratorPlanLimits struct {
	MaxManagedOrgs       int `bson:"maxManagedOrgs"`
	MaxManagedProcesses  int `bson:"maxManagedProcesses"`
	MaxManagedCensusSize int `bson:"maxManagedCensusSize"`
}

type freeIntegratorPlanDoc struct {
	Name             string                   `bson:"name"`
	Default          bool                     `bson:"default"`
	MonthlyPrice     int64                    `bson:"monthlyPrice"`
	YearlyPrice      int64                    `bson:"yearlyPrice"`
	IntegratorLimits freeIntegratorPlanLimits `bson:"integratorLimits"`
}

// upSeedFreeIntegratorPlan seeds the free integrator plan: a zero-priced plan that grants a
// small amount of managed-organization capacity. Organizations created through the integrator
// portal subscribe to it, becoming integrators on the free tier with no checkout.
//
// $setOnInsert is used so the seed runs once and never clobbers limits an operator may later
// tune directly in the database.
func upSeedFreeIntegratorPlan(ctx context.Context, database *mongo.Database) error {
	doc := freeIntegratorPlanDoc{
		Name:         "Free Integrator",
		Default:      false,
		MonthlyPrice: 0,
		YearlyPrice:  0,
		IntegratorLimits: freeIntegratorPlanLimits{
			MaxManagedOrgs:       1,
			MaxManagedProcesses:  5,
			MaxManagedCensusSize: 100,
		},
	}
	filter := bson.M{"_id": freeIntegratorPlanID}
	update := bson.M{"$setOnInsert": doc}
	opts := options.Update().SetUpsert(true)
	if _, err := database.Collection("plans").UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("failed to seed free integrator plan: %w", err)
	}
	return nil
}

func downSeedFreeIntegratorPlan(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection("plans").DeleteOne(ctx, bson.M{"_id": freeIntegratorPlanID})
	return err
}
