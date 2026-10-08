package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(27, "legacy_processes_address_index",
		upLegacyProcessesAddressIndex, downLegacyProcessesAddressIndex)
}

// legacyAddressIndexName is Mongo's default name for an ascending index on address, kept explicit
// so the down migration drops it deterministically.
const legacyAddressIndexName = "address_1"

// upLegacyProcessesAddressIndex indexes address on the legacy processes collection: the public vote
// relay looks up every envelope's election there by address (db.ProcessByAddress) before falling
// back to the questions, so without the index each envelope of an anonymous batch is a collection
// scan.
func upLegacyProcessesAddressIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{Keys: bson.D{{Key: "address", Value: 1}}}
	if _, err := database.Collection("processes").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create address index on processes: %w", err)
	}
	return nil
}

// downLegacyProcessesAddressIndex drops that index, tolerating it being already gone.
func downLegacyProcessesAddressIndex(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("processes"), []string{legacyAddressIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop address index on processes: %w", err)
	}
	return nil
}
