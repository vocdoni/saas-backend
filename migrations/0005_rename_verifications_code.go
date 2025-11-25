package migrations

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(5, "drop_verifications_and_replace_indexes",
		upDropVerificationsAndReplaceIndexes, downDropVerificationsAndReplaceIndexes)
}

func upDropVerificationsAndReplaceIndexes(ctx context.Context, database *mongo.Database) error {
	// there's no way to convert code <-> sealedCode so we set all to expired
	_, err := database.Collection("verifications").UpdateMany(ctx,
		bson.M{"code": bson.M{"$exists": true}},
		bson.M{"$set": bson.M{"expiration": time.Now()}})
	if err != nil {
		return fmt.Errorf("failed to expire verifications: %w", err)
	}

	return renameFieldAndReindex(ctx, database.Collection("verifications"), "sealedCode", "code",
		[]string{
			"code_1_type_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "sealedCode", Value: 1},
					{Key: "type", Value: 1},
				},
				Options: options.Index().SetUnique(true),
			},
		})
}

func downDropVerificationsAndReplaceIndexes(ctx context.Context, database *mongo.Database) error {
	// there's no way to convert code <-> sealedCode so we set all to expired
	_, err := database.Collection("verifications").UpdateMany(ctx,
		bson.M{"sealedCode": bson.M{"$exists": true}},
		bson.M{"$set": bson.M{"expiration": time.Now()}})
	if err != nil {
		return fmt.Errorf("failed to expire verifications: %w", err)
	}

	return renameFieldAndReindex(ctx, database.Collection("verifications"), "sealedCode", "code",
		[]string{
			"sealedCode_1_type_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "code", Value: 1},
					{Key: "type", Value: 1},
				},
				Options: options.Index().SetUnique(true),
			},
		})
}
