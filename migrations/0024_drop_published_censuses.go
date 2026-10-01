package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(24, "drop_published_censuses", upDropPublishedCensuses, downDropPublishedCensuses)
}

// upDropPublishedCensuses drops the publishedCensuses collection: created by 0001, it was never
// written by any code path. Drop is a no-op when the collection does not exist.
func upDropPublishedCensuses(ctx context.Context, database *mongo.Database) error {
	if err := database.Collection("publishedCensuses").Drop(ctx); err != nil {
		return fmt.Errorf("failed to drop publishedCensuses collection: %w", err)
	}
	return nil
}

// downDropPublishedCensuses is a no-op: the collection held no data, and nothing reads it.
func downDropPublishedCensuses(context.Context, *mongo.Database) error {
	return nil
}
