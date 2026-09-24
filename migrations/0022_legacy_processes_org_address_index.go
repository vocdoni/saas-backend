package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(22, "legacy_processes_org_address_index",
		upLegacyProcessesOrgAddressIndex, downLegacyProcessesOrgAddressIndex)
}

// legacyOrgAddressIndexName is Mongo's default name for an ascending index on orgAddress, kept
// explicit so the down migration drops it deterministically.
const legacyOrgAddressIndexName = "orgAddress_1"

// legacyOrgAddressIndexCollections are the legacy collections queried by organization; neither had
// any index but _id.
var legacyOrgAddressIndexCollections = []string{"processes", "processBundles"}

// upLegacyProcessesOrgAddressIndex indexes orgAddress on the legacy process collections: the
// projection queries them from a public, anonymous endpoint, where a collection scan is a
// denial-of-service lever. It also speeds up the existing legacy endpoints, which filter the same.
func upLegacyProcessesOrgAddressIndex(ctx context.Context, database *mongo.Database) error {
	for _, name := range legacyOrgAddressIndexCollections {
		model := mongo.IndexModel{Keys: bson.D{{Key: "orgAddress", Value: 1}}}
		if _, err := database.Collection(name).Indexes().CreateOne(ctx, model); err != nil {
			return fmt.Errorf("failed to create orgAddress index on %s: %w", name, err)
		}
	}
	return nil
}

// downLegacyProcessesOrgAddressIndex drops those indexes, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone: rolling back twice must not fail.
func downLegacyProcessesOrgAddressIndex(ctx context.Context, database *mongo.Database) error {
	for _, name := range legacyOrgAddressIndexCollections {
		if err := replaceIndex(ctx, database.Collection(name), []string{legacyOrgAddressIndexName}, nil); err != nil {
			return fmt.Errorf("failed to drop orgAddress index on %s: %w", name, err)
		}
	}
	return nil
}
