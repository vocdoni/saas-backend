package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(33, "metered_votes_org_index", upMeteredVotesOrgIndex, downMeteredVotesOrgIndex)
}

// meteredVotesOrgIndexName names the orgAddress index on meteredVotes, kept explicit so the down
// migration drops it deterministically.
const meteredVotesOrgIndexName = "metered_votes_org"

// upMeteredVotesOrgIndex indexes the metered-vote records by organization. The collection holds one
// record per metered vote, kept for good, and organization teardown and erasure delete an org's
// records by orgAddress; without the index each of those would scan the whole collection.
func upMeteredVotesOrgIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: "orgAddress", Value: 1}},
		Options: options.Index().SetName(meteredVotesOrgIndexName),
	}
	if _, err := database.Collection("meteredVotes").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create metered votes org index: %w", err)
	}
	return nil
}

// downMeteredVotesOrgIndex drops the index, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone: rolling back twice must not fail.
func downMeteredVotesOrgIndex(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("meteredVotes"), []string{meteredVotesOrgIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop metered votes org index: %w", err)
	}
	return nil
}
