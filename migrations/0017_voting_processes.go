package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(17, "voting_processes", upVotingProcesses, downVotingProcesses)
}

// upVotingProcesses creates the votingProcesses and processesQuestions collections (the
// storage of the multi-question /processes API) and their indexes: processes are listed by
// (orgAddress, published); questions are fetched by parent process, resolved by on-chain
// upstream id (vote relay / status syncer), and filtered by (orgAddress, status).
func upVotingProcesses(ctx context.Context, database *mongo.Database) error {
	for _, name := range []string{"votingProcesses", "processesQuestions"} {
		if err := database.CreateCollection(ctx, name); err != nil {
			// ignore "collection already exists" (code 48) so the migration is idempotent
			if cmdErr, ok := err.(mongo.CommandError); !ok || cmdErr.Code != 48 {
				return fmt.Errorf("failed to create %s collection: %w", name, err)
			}
		}
	}
	if _, err := database.Collection("votingProcesses").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "published", Value: 1}}}, //nolint:goconst
		// sparse: only processes currently publishing carry the marker; used by the
		// stale-publishing reconciliation scan (StaleVotingProcesses).
		{Keys: bson.D{{Key: "publishing", Value: 1}}, Options: options.Index().SetSparse(true)},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on votingProcesses: %w", err)
	}
	if _, err := database.Collection("processesQuestions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "processId", Value: 1}}},
		// upstreamId is the on-chain election id used to resolve a question (vote relay,
		// status syncer); it must be unique once set. It is omitempty (absent until the
		// question is published), so a partial index excludes unpublished questions.
		{
			Keys: bson.D{{Key: "upstreamId", Value: 1}},
			Options: options.Index().SetUnique(true).
				SetPartialFilterExpression(bson.M{"upstreamId": bson.M{"$exists": true}}), //nolint:goconst
		},
		{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "status", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on processesQuestions: %w", err)
	}
	return nil
}

func downVotingProcesses(context.Context, *mongo.Database) error {
	// Dropping these collections would destroy live data; matching the repo policy for
	// data-bearing collections we do nothing here. upVotingProcesses is idempotent.
	return nil
}
