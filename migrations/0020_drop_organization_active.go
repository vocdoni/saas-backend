package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(20, "drop_organization_active", upDropOrganizationActive, downDropOrganizationActive)
}

// upDropOrganizationActive removes the organizations' `active` field (issue #625). Nothing ever
// read it: no handler, middleware or subscription check gated on it, so an organization stored
// with active=false stayed fully operational. Deactivating one deliberately was possible — the
// partial update handler had a branch for it — but a stored false does not prove that intent: the
// same branch compared a plain bool, so a PUT body merely omitting `active` decoded to false and
// was force-persisted, leaving healthy organizations misreporting themselves. Since the two cases
// are indistinguishable and neither was enforced, the field is gone from the model and this drops
// every stale value with it, logging the addresses it strips a false from.
func upDropOrganizationActive(ctx context.Context, database *mongo.Database) error {
	var deactivated []any
	if err := database.Collection("organizations").
		Distinct(ctx, "_id", bson.M{"active": false}).Decode(&deactivated); err != nil {
		return fmt.Errorf("failed to list deactivated organizations: %w", err)
	}
	if len(deactivated) > 0 {
		log.Infow("dropping stored organization active=false", "organizations", deactivated)
	}
	res, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"active": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"active": ""}},
	)
	if err != nil {
		return fmt.Errorf("failed to unset active on organizations: %w", err)
	}
	log.Infow("dropped organization active field", "organizations", res.ModifiedCount)
	return nil
}

// downDropOrganizationActive restores the field as true on every organization: it is the value
// both creation paths hardcoded, and a false surviving a partial up (or written by an old binary
// mid-deploy) is the misreporting this migration exists to remove, so it is overwritten too.
func downDropOrganizationActive(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{},
		bson.M{"$set": bson.M{"active": true}},
	); err != nil {
		return fmt.Errorf("failed to restore active on organizations: %w", err)
	}
	return nil
}
