package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(19, "drop_organization_active", upDropOrganizationActive, downDropOrganizationActive)
}

// upDropOrganizationActive removes the organizations' `active` field (issue #625). Nothing ever
// read it: no handler, middleware or subscription check gated on it, so an organization stored
// with active=false stayed fully operational. The only writer that ever set it to false was a bug
// — the partial update handler compared a plain bool, so a PUT body omitting `active` decoded to
// false and was force-persisted — which left healthy organizations misreporting themselves to
// clients. The field is gone from the model; this drops the stale values with it.
func upDropOrganizationActive(ctx context.Context, database *mongo.Database) error {
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

// downDropOrganizationActive restores the field as true, the only value the code ever wrote
// deliberately: both creation paths hardcoded it, and every false was the bug described above.
func downDropOrganizationActive(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"active": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"active": true}},
	); err != nil {
		return fmt.Errorf("failed to restore active on organizations: %w", err)
	}
	return nil
}
