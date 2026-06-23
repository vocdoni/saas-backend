package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(13, "api_keys", upAPIKeys, downAPIKeys)
}

// upAPIKeys creates the apiKeys collection and its indexes: a unique index on the secret hash
// (for auth lookups) and an index on the owning organization address (for listing).
func upAPIKeys(ctx context.Context, database *mongo.Database) error {
	if err := database.CreateCollection(ctx, "apiKeys"); err != nil {
		// ignore "collection already exists" so the migration is idempotent
		if cmdErr, ok := err.(mongo.CommandError); !ok || cmdErr.Code != 48 {
			return fmt.Errorf("failed to create apiKeys collection: %w", err)
		}
	}
	coll := database.Collection("apiKeys")
	if _, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "hash", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "orgAddress", Value: 1}},
		},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on apiKeys: %w", err)
	}
	return nil
}

func downAPIKeys(context.Context, *mongo.Database) error {
	// Dropping apiKeys would destroy live credentials; matching the repo policy for data-bearing
	// collections we do nothing here. upAPIKeys is idempotent.
	return nil
}
