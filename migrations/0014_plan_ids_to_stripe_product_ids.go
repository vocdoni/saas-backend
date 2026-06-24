package migrations

import (
	"context"
	"fmt"
	"os"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(14, "plan_ids_to_stripe_product_ids", upPlanIDsToStripeProductIDs, downPlanIDsToStripeProductIDs)
}

// defaultFreeIntegratorProductID is the Stripe product backing the free integrator tier in the
// vocdoni sandbox. It can be overridden per environment via STRIPE_FREE_INTEGRATOR_PRODUCT_ID
// (e.g. for production, where the product ID differs). It is only used to remap organizations
// that were on the old Mongo-seeded free integrator plan (see migration 0012).
const defaultFreeIntegratorProductID = "prod_UkaJ8hyy6juz5e"

// oldFreeIntegratorPlanID is the sentinel integer _id that migration 0012 used for the
// Mongo-seeded free integrator plan.
const oldFreeIntegratorPlanID int64 = 9001

// planIDMigrationOrgSubscription is the minimal projection of an organization's subscription
// needed to rewrite its plan ID. PlanID is decoded as `any` because it may be either the old
// integer value or, on an idempotent re-run, the new string value.
type planIDMigrationOrgSubscription struct {
	PlanID any `bson:"planID"`
}

// planIDMigrationOrg is the minimal projection of an organization needed by this migration.
type planIDMigrationOrg struct {
	ID           any                            `bson:"_id"`
	Subscription planIDMigrationOrgSubscription `bson:"subscription"`
}

// planIDMigrationPlan is the minimal projection of a plan document used to build the
// old-integer-ID -> Stripe product ID map.
type planIDMigrationPlan struct {
	ID       any    `bson:"_id"`
	StripeID string `bson:"stripeID"`
}

// upPlanIDsToStripeProductIDs migrates plan identity from synthetic integer IDs to Stripe
// product IDs (strings), making Stripe the single source of truth for plan definitions.
//
// It rewrites every organization's subscription.planID from its old integer value to the
// corresponding Stripe product ID, then drops the now-stale integer-keyed plan documents. The
// plans collection is repopulated with string-keyed documents by the Stripe sync that runs at
// startup (stripe.Service.GetPlansFromStripe), so no plan documents are recreated here.
func upPlanIDsToStripeProductIDs(ctx context.Context, database *mongo.Database) error {
	plansColl := database.Collection("plans")
	orgsColl := database.Collection("organizations")

	// Build the old-integer-ID -> Stripe product ID map from the existing plan documents. A
	// long-running database has the real stripeID on each synced plan; a fresh database only has
	// stubs (no stripeID) but also has no subscribed organizations, so the map is simply empty.
	idMap := map[int64]string{}
	cursor, err := plansColl.Find(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("failed to list plans: %w", err)
	}
	for cursor.Next(ctx) {
		var p planIDMigrationPlan
		if err := cursor.Decode(&p); err != nil {
			_ = cursor.Close(ctx)
			return fmt.Errorf("failed to decode plan: %w", err)
		}
		id, ok := toInt64(p.ID)
		if !ok || p.StripeID == "" {
			continue
		}
		idMap[id] = p.StripeID
	}
	if err := cursor.Err(); err != nil {
		_ = cursor.Close(ctx)
		return fmt.Errorf("failed to iterate plans: %w", err)
	}
	if err := cursor.Close(ctx); err != nil {
		return fmt.Errorf("failed to close plans cursor: %w", err)
	}

	// Map the old Mongo-seeded free integrator plan to its Stripe product (env-overridable).
	freeProductID := os.Getenv("STRIPE_FREE_INTEGRATOR_PRODUCT_ID")
	if freeProductID == "" {
		freeProductID = defaultFreeIntegratorProductID
	}
	if freeProductID != "" {
		idMap[oldFreeIntegratorPlanID] = freeProductID
	}

	// Rewrite each organization's subscription.planID to the new string value.
	orgCursor, err := orgsColl.Find(ctx, bson.D{})
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}
	defer func() { _ = orgCursor.Close(ctx) }()

	for orgCursor.Next(ctx) {
		var org planIDMigrationOrg
		if err := orgCursor.Decode(&org); err != nil {
			return fmt.Errorf("failed to decode organization: %w", err)
		}

		// Already a string (idempotent re-run) — nothing to do.
		if _, isString := org.Subscription.PlanID.(string); isString {
			continue
		}

		newPlanID := ""
		if oldID, ok := toInt64(org.Subscription.PlanID); ok && oldID != 0 {
			mapped, found := idMap[oldID]
			if !found {
				log.Warnw("migration 0014: organization subscription references an unknown plan; clearing it",
					"org", fmt.Sprintf("%v", org.ID), "oldPlanID", oldID)
			}
			newPlanID = mapped
		}

		if _, err := orgsColl.UpdateOne(ctx,
			bson.M{"_id": org.ID},
			bson.M{"$set": bson.M{"subscription.planID": newPlanID}},
		); err != nil {
			return fmt.Errorf("failed to update organization %v subscription plan: %w", org.ID, err)
		}
	}
	if err := orgCursor.Err(); err != nil {
		return err
	}

	// Drop the stale integer-keyed plan documents (the initial stubs and the seeded free
	// integrator plan). String-keyed plans are (re)created from Stripe at startup.
	if _, err := plansColl.DeleteMany(ctx, bson.M{"_id": bson.M{"$type": []string{"int", "long"}}}); err != nil {
		return fmt.Errorf("failed to delete integer-keyed plans: %w", err)
	}

	return nil
}

// downPlanIDsToStripeProductIDs is a no-op: this is a forward-only data migration. The original
// integer plan IDs are not recoverable from the string product IDs, and the plans collection is
// rebuilt from Stripe at startup regardless.
func downPlanIDsToStripeProductIDs(_ context.Context, _ *mongo.Database) error {
	return nil
}

// toInt64 converts a BSON numeric value (int32 or int64) to int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}
