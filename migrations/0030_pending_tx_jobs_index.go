package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(30, "pending_tx_jobs_index", upPendingTxJobsIndex, downPendingTxJobsIndex)
}

// pendingTxJobsIndexName names the partial index on pending transaction jobs, kept explicit so
// the down migration drops it deterministically.
const pendingTxJobsIndexName = "pending_tx_jobs"

// upPendingTxJobsIndex indexes the transaction jobs still pending. Every replica heartbeats its
// own pending jobs and sweeps the ones whose owner stopped heartbeating, both periodically; the
// jobs collection keeps every vote relay ever made, so without this those queries would scan it
// whole every time. The index is partial, so it only ever holds the handful of in-flight jobs.
func upPendingTxJobsIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "status", Value: 1}, {Key: "instance", Value: 1}},
		Options: options.Index().
			SetName(pendingTxJobsIndexName).
			SetPartialFilterExpression(bson.M{"status": "pending"}),
	}
	if _, err := database.Collection("jobs").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create pending tx jobs index: %w", err)
	}
	return nil
}

// downPendingTxJobsIndex drops the index, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone: rolling back twice must not fail.
func downPendingTxJobsIndex(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("jobs"), []string{pendingTxJobsIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop pending tx jobs index: %w", err)
	}
	return nil
}
