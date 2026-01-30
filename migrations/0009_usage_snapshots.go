package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const usageSnapshotsIndexName = "orgAddress_periodStart_unique"

func init() {
	AddMigration(9, "usage_snapshots", upUsageSnapshots, downUsageSnapshots)
}

func upUsageSnapshots(ctx context.Context, database *mongo.Database) error {
	collection := database.Collection("orgUsageSnapshots")

	_, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1},
			{Key: "periodStart", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName(usageSnapshotsIndexName),
	})
	if err != nil {
		return fmt.Errorf("failed to create usage snapshot index: %w", err)
	}
	return nil
}

func downUsageSnapshots(ctx context.Context, database *mongo.Database) error {
	collection := database.Collection("orgUsageSnapshots")
	if _, err := collection.Indexes().DropOne(ctx, usageSnapshotsIndexName); err != nil {
		return fmt.Errorf("failed to drop usage snapshot index: %w", err)
	}
	return nil
}
