package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(4, "drop_jobid_index", upDropJobIDIndex, downDropJobIDIndex)
}

func upDropJobIDIndex(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection("jobs").Indexes().DropOne(ctx, "jobId_1")
	return err
}

func downDropJobIDIndex(ctx context.Context, database *mongo.Database) error {
	// create an index for the jobId field on jobs (must be unique)
	_, err := database.Collection("jobs").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "jobId", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	})
	return err
}
