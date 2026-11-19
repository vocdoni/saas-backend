package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(38, "cert_auth_assertions_indexes", upCertAuthAssertionsIndexes, downCertAuthAssertionsIndexes)
}

// certAuthAssertionsIndexNames are Mongo's default names of the indexes below, kept explicit so the
// down migration drops them deterministically.
var certAuthAssertionsIndexNames = []string{"codeHash_1", "expiresAt_1"}

// upCertAuthAssertionsIndexes indexes the certificate auth assertions: a unique code hash, which
// step 0 looks the assertion up by, and a TTL on expiresAt so expired assertions are removed.
func upCertAuthAssertionsIndexes(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection("certAuthAssertions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "codeHash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err != nil {
		return fmt.Errorf("failed to create certAuthAssertions indexes: %w", err)
	}
	return nil
}

// downCertAuthAssertionsIndexes drops those indexes, tolerating them being already gone.
func downCertAuthAssertionsIndexes(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("certAuthAssertions"), certAuthAssertionsIndexNames, nil); err != nil {
		return fmt.Errorf("failed to drop certAuthAssertions indexes: %w", err)
	}
	return nil
}
