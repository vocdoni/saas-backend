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

// legacyOrgAddressIndexName is the default name Mongo assigns to a single-field ascending index on
// orgAddress; kept explicit so the down migration can drop it deterministically.
const legacyOrgAddressIndexName = "orgAddress_1"

// legacyOrgAddressIndexCollections are the two legacy collections queried by organization: neither
// had any index but _id (0002_initial_indexes creates none for them).
var legacyOrgAddressIndexCollections = []string{"processes", "processBundles"}

// upLegacyProcessesOrgAddressIndex indexes orgAddress on the legacy process collections. The
// /processes read path now projects those records for the requested organization, which puts these
// queries on a public, anonymous endpoint — a collection scan there is a denial-of-service lever.
// It also speeds up the existing legacy endpoints, which filter the same way.
func upLegacyProcessesOrgAddressIndex(ctx context.Context, database *mongo.Database) error {
	for _, name := range legacyOrgAddressIndexCollections {
		model := mongo.IndexModel{Keys: bson.D{{Key: "orgAddress", Value: 1}}}
		if _, err := database.Collection(name).Indexes().CreateOne(ctx, model); err != nil {
			return fmt.Errorf("failed to create orgAddress index on %s: %w", name, err)
		}
	}
	return nil
}

// downLegacyProcessesOrgAddressIndex drops those indexes. Dropping an index does not touch the
// documents, so unlike data-bearing migrations this rollback is safe to perform. It goes through
// replaceIndex, which tolerates an index that is already gone — rolling back twice, or rolling back
// a partially applied up, must not fail.
func downLegacyProcessesOrgAddressIndex(ctx context.Context, database *mongo.Database) error {
	for _, name := range legacyOrgAddressIndexCollections {
		if err := replaceIndex(ctx, database.Collection(name), []string{legacyOrgAddressIndexName}, nil); err != nil {
			return fmt.Errorf("failed to drop orgAddress index on %s: %w", name, err)
		}
	}
	return nil
}
