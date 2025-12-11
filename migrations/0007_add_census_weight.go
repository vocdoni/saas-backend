package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(7, "add_census_weight", upAddCensusWeight, downAddCensusWeight)
}

func upAddCensusWeight(ctx context.Context, database *mongo.Database) error {
	// add weight field with default 1 to all org members
	_, err := database.Collection("orgMembers").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weight": 1}})
	if err != nil {
		return fmt.Errorf("failed to add weight field to org members: %w", err)
	}

	// add weight field with default false to all censuses
	// if the field already exists, do not overwrite
	_, err = database.Collection("census").UpdateMany(ctx,
		bson.M{"weighted": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weighted": false}})
	if err != nil {
		return fmt.Errorf("failed to add weight field to censuses: %w", err)
	}

	// add weight field with default 1 to all csp auth tokens
	_, err = database.Collection("cspTokens").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weight": 1}})
	if err != nil {
		return fmt.Errorf("failed to add weight field to csp auth tokens: %w", err)
	}
	return nil
}

func downAddCensusWeight(_ context.Context, _ *mongo.Database) error {
	// we do not remove anything to avoid data loss
	return nil
}
