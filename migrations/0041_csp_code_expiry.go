package migrations

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(41, "csp_code_expiry", upCSPCodeExpiry, downCSPCodeExpiry)
}

// cspTokensTTLIndexName names the TTL index on unverified cspTokens codes, kept explicit so the
// down migration drops it deterministically.
const cspTokensTTLIndexName = "csp_tokens_unverified_ttl"

// cspCodeRetention is how long an unverified code is kept from its creation: any OTP TTL up to 1 h
// (the default is 15 min) plus a day, so the latest code still reads (LastCSPAuth, the step-0
// cooldown) for longer than any cooldown.
const cspCodeRetention = 25 * time.Hour

// upCSPCodeExpiry creates a TTL index on createdat restricted to codes without verifiedby, so
// unverified codes and their secrets are deleted a day after they expire, while verified codes,
// which signing reads, are never in the index. Existing documents are covered as they are.
func upCSPCodeExpiry(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "createdat", Value: 1}},
		Options: options.Index().
			SetName(cspTokensTTLIndexName).
			SetExpireAfterSeconds(int32(cspCodeRetention.Seconds())).
			// matches a missing verifiedby; it is never stored as null
			SetPartialFilterExpression(bson.M{"verifiedby": nil}),
	}
	if _, err := database.Collection("cspTokens").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create csp tokens ttl index: %w", err)
	}
	return nil
}

// downCSPCodeExpiry drops the TTL index, tolerating it being already gone.
func downCSPCodeExpiry(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspTokens"), []string{cspTokensTTLIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop csp tokens ttl index: %w", err)
	}
	return nil
}
