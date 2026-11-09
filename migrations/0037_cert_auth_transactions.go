package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(37, "cert_auth_transactions", upCertAuthTransactions, downCertAuthTransactions)
}

// certAuthTransactionsIndexes are Mongo's default names of the indexes migration 37 creates, kept
// explicit so the down migration drops them deterministically.
var certAuthTransactionsIndexes = []string{"state_1", "code_1", "expiresAt_1"}

// upCertAuthTransactions indexes the certificate sign-in transactions: one attempt per state, one
// per code once the auth listener issued it (sparse: a created attempt has none), and a TTL on
// expiresAt that reaps every attempt, and the DER chain it may hold, once it expired. Creating the
// indexes creates the collection; running it again is a no-op.
func upCertAuthTransactions(ctx context.Context, database *mongo.Database) error {
	models := []mongo.IndexModel{
		{Keys: bson.D{{Key: "state", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "code", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	}
	if _, err := database.Collection("certAuthTransactions").Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("failed to create indexes on certAuthTransactions: %w", err)
	}
	return nil
}

// downCertAuthTransactions drops those indexes, tolerating them being already gone. The documents
// are short-lived attempts, so nothing else is undone.
func downCertAuthTransactions(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("certAuthTransactions"), certAuthTransactionsIndexes, nil); err != nil {
		return fmt.Errorf("failed to drop indexes on certAuthTransactions: %w", err)
	}
	return nil
}
