package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(30, "csp_counters_ttl_index",
		upCSPCountersTTLIndex, downCSPCountersTTLIndex)
}

// cspCountersTTLIndexName is Mongo's default name for an ascending index on expiresat, kept
// explicit so the down migration drops it deterministically.
const cspCountersTTLIndexName = "expiresat_1"

// upCSPCountersTTLIndex adds a TTL index on the daily CSP counters (challenge sends and failed
// step-0 authentications), so expired days are garbage collected instead of accumulating one
// document per member per day forever.
func upCSPCountersTTLIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: "expiresat", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	}
	if _, err := database.Collection("cspCounters").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create ttl index on cspCounters: %w", err)
	}
	return nil
}

// downCSPCountersTTLIndex drops that index, tolerating it being already gone.
func downCSPCountersTTLIndex(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspCounters"), []string{cspCountersTTLIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop ttl index on cspCounters: %w", err)
	}
	return nil
}
