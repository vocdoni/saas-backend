package migrations

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(11, "create_all_members_auto_group", upCreateAllMembersAutoGroup, downCreateAllMembersAutoGroup)
}

// upCreateAllMembersAutoGroup creates the "All members" auto group for every
// organization that already has at least one member but does not yet have an
// auto group.  New organizations receive their auto group automatically when
// the first member is added via the application layer, so this migration only
// needs to back-fill existing data.
func upCreateAllMembersAutoGroup(ctx context.Context, database *mongo.Database) error {
	organizations := database.Collection("organizations")
	orgMembers := database.Collection("orgMembers")
	orgMemberGroups := database.Collection("orgMemberGroups")

	// Fetch all org addresses from the organizations collection.
	// This is more efficient than Distinct on orgMembers for large member bases,
	// since the organizations collection is far smaller.
	cursor, err := organizations.Find(ctx, bson.D{}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var org struct {
			ID interface{} `bson:"_id"`
		}
		if err := cursor.Decode(&org); err != nil {
			return fmt.Errorf("failed to decode organization: %w", err)
		}
		addr := org.ID

		// Skip orgs that have no members yet.
		memberCount, err := orgMembers.CountDocuments(ctx,
			bson.M{"orgAddress": addr},
			options.Count().SetLimit(1),
		)
		if err != nil {
			return fmt.Errorf("failed to count members for %v: %w", addr, err)
		}
		if memberCount == 0 {
			continue
		}
		// Check whether an auto group already exists for this org (idempotent).
		count, err := orgMemberGroups.CountDocuments(ctx,
			bson.M{"orgAddress": addr, "isAutoGroup": true},
			options.Count().SetLimit(1),
		)
		if err != nil {
			return fmt.Errorf("failed to check existing auto group for %v: %w", addr, err)
		}
		if count > 0 {
			continue // already has an auto group, skip
		}

		now := time.Now()
		group := bson.M{
			"_id":         primitive.NewObjectID(),
			"orgAddress":  addr,
			"title":       "All members",
			"description": "This group is automatically generated and always contains every member of your member base.",
			"memberIds":   []string{},
			"censusIds":   []string{},
			"isAutoGroup": true,
			"createdAt":   now,
			"updatedAt":   now,
		}

		if _, err := orgMemberGroups.InsertOne(ctx, group); err != nil {
			return fmt.Errorf("failed to insert auto group for %v: %w", addr, err)
		}
	}

	return cursor.Err()
}

// downCreateAllMembersAutoGroup deletes all auto groups created by this migration.
// It is safe to run multiple times.
func downCreateAllMembersAutoGroup(ctx context.Context, database *mongo.Database) error {
	orgMemberGroups := database.Collection("orgMemberGroups")
	_, err := orgMemberGroups.DeleteMany(ctx, bson.M{"isAutoGroup": true})
	if err != nil {
		return fmt.Errorf("failed to delete auto groups: %w", err)
	}
	return nil
}
