package migrations

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// verificationsUserTypeIndex is the name of the unique (userId, type) index on verifications.
const verificationsUserTypeIndex = "userId_1_type_1"

func init() {
	AddMigration(27, "verifications_per_user_type", upVerificationsPerUserType, downVerificationsPerUserType)
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
	// enforce a single code per (user, type). The index is partial on userId existing: during a
	// rolling deploy, replicas still running the one-per-user scheme write documents without
	// userId, which a plain unique index would treat as (null, type) and reject from the second
	// one on (E11000).
	if _, err := verifications.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userId", Value: 1},
			{Key: "type", Value: 1},
		},
		Options: options.Index().
			SetName(verificationsUserTypeIndex).
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"userId": bson.M{"$exists": true}}),
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
	if err := verifications.Indexes().DropOne(ctx, verificationsUserTypeIndex); err != nil {
		// an index that is already gone is not a failure: IndexNotFound (27), or
		// NamespaceNotFound (26) when the collection itself is missing
		var cmdErr mongo.CommandError
		if !errors.As(err, &cmdErr) || (cmdErr.Code != 27 && cmdErr.Code != 26) {
			return fmt.Errorf("failed to drop (userId, type) index on verifications: %w", err)
		}
	}
	return nil
}
