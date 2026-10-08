package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(27, "voting_processes_upstream_id_index",
		upVotingProcessesUpstreamIDIndex, downVotingProcessesUpstreamIDIndex)
}

// votingProcessesUpstreamIDIndexName is Mongo's default name for an ascending index on upstreamId,
// kept explicit so the down migration drops it deterministically.
const votingProcessesUpstreamIDIndexName = "upstreamId_1"

// upVotingProcessesUpstreamIDIndex indexes the on-chain id of each process's parent election, which
// the status syncer resolves the process by. It must be unique once set; the field is absent until
// the parent election is published, so a partial index leaves drafts out.
func upVotingProcessesUpstreamIDIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "upstreamId", Value: 1}},
		Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"upstreamId": bson.M{"$exists": true}}),
	}
	if _, err := database.Collection("votingProcesses").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create upstreamId index on votingProcesses: %w", err)
	}
	return nil
}

// downVotingProcessesUpstreamIDIndex drops that index, which touches no document. replaceIndex
// tolerates an index already gone, so rolling back twice does not fail.
func downVotingProcessesUpstreamIDIndex(ctx context.Context, database *mongo.Database) error {
	err := replaceIndex(ctx, database.Collection("votingProcesses"), []string{votingProcessesUpstreamIDIndexName}, nil)
	if err != nil {
		return fmt.Errorf("failed to drop upstreamId index on votingProcesses: %w", err)
	}
	return nil
}
