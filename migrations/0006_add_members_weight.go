package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(6, "add_members_weight", upAddMembersWeight, downAddMembersWeight)
}

func upAddMembersWeight(ctx context.Context, database *mongo.Database) error {
	// add weight field with default 1 to all orgMembers
	// if the field already exists, do not overwrite
	_, err := database.Collection("orgMembers").UpdateMany(ctx,
		bson.M{"weight": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"weight": 1}})
	if err != nil {
		return fmt.Errorf("failed to add weight to orgMembers: %w", err)
	}
	return nil
}

func downAddMembersWeight(_ context.Context, _ *mongo.Database) error {
	// we do not remove anything to avoid data loss
	return nil
}
