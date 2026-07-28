package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(18, "census_revocation_indexes", upCensusRevocationIndexes, downCensusRevocationIndexes)
}

// upCensusRevocationIndexes indexes the two lookups that census revocation introduced, both of
// which are collection scans without them:
//
//   - cspTokens.userid — revoking a member deletes their auth tokens by user id, and this
//     collection had no index at all, so every removal scanned every token ever issued.
//   - processesQuestions.eligibleMemberIds — deleting a member prunes them from every question's
//     eligibility subset, which is an unscoped update across the whole collection. Multikey, since
//     the field is an array.
func upCensusRevocationIndexes(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("cspTokens").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "userid", Value: 1}},
	}); err != nil {
		return fmt.Errorf("failed to create index on userid for cspTokens: %w", err)
	}
	if _, err := database.Collection("processesQuestions").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "eligibleMemberIds", Value: 1}},
	}); err != nil {
		return fmt.Errorf("failed to create index on eligibleMemberIds for processesQuestions: %w", err)
	}
	return nil
}

func downCensusRevocationIndexes(ctx context.Context, database *mongo.Database) error {
	// indexes only, so dropping them is safe and loses no data
	if _, err := database.Collection("cspTokens").Indexes().DropOne(ctx, "userid_1"); err != nil {
		return fmt.Errorf("failed to drop index on userid for cspTokens: %w", err)
	}
	if _, err := database.Collection("processesQuestions").Indexes().DropOne(ctx, "eligibleMemberIds_1"); err != nil {
		return fmt.Errorf("failed to drop index on eligibleMemberIds for processesQuestions: %w", err)
	}
	return nil
}
