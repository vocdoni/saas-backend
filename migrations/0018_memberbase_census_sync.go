package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(18, "memberbase_census_sync", upMemberbaseCensusSync, downMemberbaseCensusSync)
}

// upMemberbaseCensusSync adds the indexes behind the access paths that keep process censuses in
// sync with the memberbase. Every memberbase change now has to reach the censuses and elections
// built from it, and each of these lookups was a collection scan before.
func upMemberbaseCensusSync(ctx context.Context, database *mongo.Database) error {
	// cspTokens had no indexes at all, so revoking a member's auth sessions — and the existing
	// session cleanup — scanned the whole collection.
	if _, err := database.Collection("cspTokens").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "userid", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on cspTokens: %w", err)
	}
	// multikey: the $pull that prunes a removed member from every question eligibility list, and
	// the lookup of the questions naming them.
	if _, err := database.Collection("processesQuestions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "eligibleMemberIds", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on processesQuestions: %w", err)
	}
	// the new access path: from a census back to the processes built on it.
	if _, err := database.Collection("votingProcesses").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "censusId", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on votingProcesses: %w", err)
	}
	return nil
}

// downMemberbaseCensusSync drops the indexes again. They carry no data, so unlike the
// data-bearing collections this migration is genuinely reversible.
func downMemberbaseCensusSync(ctx context.Context, database *mongo.Database) error {
	for _, idx := range []struct {
		collection string
		name       string
	}{
		{"cspTokens", "userid_1"},
		{"processesQuestions", "eligibleMemberIds_1"},
		{"votingProcesses", "censusId_1"},
	} {
		if _, err := database.Collection(idx.collection).Indexes().DropOne(ctx, idx.name); err != nil {
			// IndexNotFound (27): the migration never got far enough to create it
			if cmdErr, ok := err.(mongo.CommandError); !ok || cmdErr.Code != 27 {
				return fmt.Errorf("failed to drop index %s on %s: %w", idx.name, idx.collection, err)
			}
		}
	}
	return nil
}
