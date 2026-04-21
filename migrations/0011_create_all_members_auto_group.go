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
	orgMembers := database.Collection("orgMembers")
	orgMemberGroups := database.Collection("orgMemberGroups")

	// Find all distinct orgAddress values that have at least one member.
	addresses, err := orgMembers.Distinct(ctx, "orgAddress", bson.D{})
	if err != nil {
		return fmt.Errorf("failed to list distinct org addresses: %w", err)
	}

	for _, addr := range addresses {
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

	return nil
}

// downCreateAllMembersAutoGroup removes all auto groups created by this migration.
// It is safe to run multiple times.
func downCreateAllMembersAutoGroup(_ context.Context, _ *mongo.Database) error {
	// We intentionally do not delete auto groups on rollback to avoid data loss:
	// the groups are harmless and may already be referenced by census records.
	return nil
}
