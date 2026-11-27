package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(42, "csp_send_backoff", upCSPSendBackoff, downCSPSendBackoff)
}

// cspSendBackoffTTLIndexName names the TTL index on the cspSendBackoff expiry, kept explicit so the
// down migration drops it deterministically.
const cspSendBackoffTTLIndexName = "csp_send_backoff_expires_at"

// upCSPSendBackoff indexes the expiry of the per-member challenge-send backoff with a TTL index, so a
// backoff is deleted a day after its latest send, once every send in it has aged out.
func upCSPSendBackoff(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: "expiresat", Value: 1}},
		Options: options.Index().SetName(cspSendBackoffTTLIndexName).SetExpireAfterSeconds(0),
	}
	if _, err := database.Collection("cspSendBackoff").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create csp send backoff ttl index: %w", err)
	}
	return nil
}

// downCSPSendBackoff drops the TTL index, tolerating it being already gone.
func downCSPSendBackoff(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspSendBackoff"), []string{cspSendBackoffTTLIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop csp send backoff ttl index: %w", err)
	}
	return nil
}
