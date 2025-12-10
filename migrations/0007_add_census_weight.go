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
	// add weight field with default false to all censuses
	// if the field already exists, do not overwrite
	_, err := database.Collection("censuses").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weight": false}})
	if err != nil {
		return fmt.Errorf("failed to add weight field to censuses: %w", err)
	}

	// add weight field with default 1 to all csp auth tokens
	_, err = database.Collection("csp_auth_tokens").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weight": 1}})
	if err != nil {
		return fmt.Errorf("failed to add weight field to csp auth tokens: %w", err)
	}
	return nil
}

func downAddCensusWeight(ctx context.Context, database *mongo.Database) error {
	// remove weight field from all csp auth tokens
	_, err := database.Collection("csp_auth_tokens").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"weight": ""}})
	if err != nil {
		return fmt.Errorf("failed to remove weight from csp auth tokens: %w", err)
	}

	// remove weight field from all censuses
	// TODO check if this is the desired behavior since data will be lost
	_, err = database.Collection("censuses").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"weight": ""}})
	if err != nil {
		return fmt.Errorf("failed to remove weight from censuses: %w", err)
	}
	return nil
}
