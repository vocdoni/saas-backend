package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(16, "organizations_managed_by_index", upOrganizationsManagedByIndex, downOrganizationsManagedByIndex)
}

// managedByIndexName is the default name Mongo assigns to a single-field ascending index on
// managedBy; kept explicit so the down migration can drop it deterministically.
const managedByIndexName = "managedBy_1"

// upOrganizationsManagedByIndex indexes organizations.managedBy. Every integrator path filters
// organizations by managedBy (listing managed orgs, summing their shared-pool counters), and
// /integrator is polled frequently from the dashboard, so without this index those queries are
// collection scans. The index is partial — only documents that actually carry managedBy (the
// managed orgs) are indexed — because the field is omitempty and absent on standalone orgs.
func upOrganizationsManagedByIndex(ctx context.Context, database *mongo.Database) error {
	coll := database.Collection("organizations")
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "managedBy", Value: 1}},
		// Only index documents that carry managedBy. An equality match on a concrete integrator
		// address implies managedBy exists, so the planner can still serve those queries from
		// this partial index while standalone orgs stay out of it.
		Options: options.Index().SetPartialFilterExpression(bson.M{"managedBy": bson.M{"$exists": true}}),
	}
	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create managedBy index on organizations: %w", err)
	}
	return nil
}

// downOrganizationsManagedByIndex drops the managedBy index. Dropping an index is non-destructive
// to the documents, so unlike data-bearing collections this rollback is safe to perform.
func downOrganizationsManagedByIndex(ctx context.Context, database *mongo.Database) error {
	coll := database.Collection("organizations")
	if _, err := coll.Indexes().DropOne(ctx, managedByIndexName); err != nil {
		return fmt.Errorf("failed to drop managedBy index on organizations: %w", err)
	}
	return nil
}
