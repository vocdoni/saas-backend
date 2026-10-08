package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(28, "verifications_per_user_type", upVerificationsPerUserType, downVerificationsPerUserType)
}

// upVerificationsPerUserType moves user verification codes from one-document-per-user
// (keyed by _id = user ID, so a password-reset code silently replaced a pending
// account-verification code and vice versa) to one document per (user, type), keyed
// by a userId field with a unique (userId, type) index. Codes are short-lived
// one-time secrets with no way to translate the old _id key in place, so pending
// codes are dropped (users simply request a new one), mirroring migration 0005.
func upVerificationsPerUserType(ctx context.Context, database *mongo.Database) error {
	verifications := database.Collection("verifications")
	// drop the pending codes: the old documents are keyed by _id = user ID and cannot
	// coexist with the new per-(user, type) scheme
	if _, err := verifications.DeleteMany(ctx, bson.M{"userId": bson.M{"$exists": false}}); err != nil {
		return fmt.Errorf("failed to drop legacy verification codes: %w", err)
	}
	// enforce a single code per (user, type)
	if _, err := verifications.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userId", Value: 1},
			{Key: "type", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create unique (userId, type) index on verifications: %w", err)
	}
	return nil
}

func downVerificationsPerUserType(ctx context.Context, database *mongo.Database) error {
	verifications := database.Collection("verifications")
	// drop the per-(user, type) codes: they cannot be folded back into the one-per-user scheme
	if _, err := verifications.DeleteMany(ctx, bson.M{"userId": bson.M{"$exists": true}}); err != nil {
		return fmt.Errorf("failed to drop per-type verification codes: %w", err)
	}
	if err := verifications.Indexes().DropOne(ctx, "userId_1_type_1"); err != nil {
		return fmt.Errorf("failed to drop (userId, type) index on verifications: %w", err)
	}
	return nil
}
