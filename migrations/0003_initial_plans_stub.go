package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(3, "initial_plans_stub", upInitialPlansStub, downInitialPlansStub)
}

type plan struct {
	ID uint64 `json:"id" bson:"_id"`
}

// plans contains 4 plans stubs, so they can be overwritten
// with SetPlan when stripe is initialized.
var plans = []plan{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}

func upInitialPlansStub(ctx context.Context, database *mongo.Database) error {
	for _, p := range plans {
		filter := bson.M{"_id": p.ID}
		update := bson.M{"$set": p}
		opts := options.Update().SetUpsert(true)
		if _, err := database.Collection("plans").UpdateOne(ctx, filter, update, opts); err != nil {
			return fmt.Errorf("failed to upsert plan with ID %d: %w", p.ID, err)
		}
	}
	return nil
}

func downInitialPlansStub(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection("plans").DeleteMany(ctx, bson.D{})
	return err
}
