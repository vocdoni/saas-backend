package migrations

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(26, "payg_billing", upPaygBilling, downPaygBilling)
}

// upPaygBilling creates processPayments (keyed by process id), wallets (keyed by org) and
// walletLedger, whose unique idempotencyKey index stops a replay being recorded twice.
func upPaygBilling(ctx context.Context, database *mongo.Database) error {
	for _, name := range []string{"processPayments", "wallets", "walletLedger"} {
		if err := database.CreateCollection(ctx, name); err != nil {
			// ignore "collection already exists" (code 48) so the migration is idempotent
			var cmdErr mongo.CommandError
			if !errors.As(err, &cmdErr) || cmdErr.Code != 48 {
				return fmt.Errorf("failed to create %s collection: %w", name, err)
			}
		}
	}
	ledgerIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "idempotencyKey", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "createdAt", Value: -1}}},
	}
	if _, err := database.Collection("walletLedger").Indexes().CreateMany(ctx, ledgerIndexes); err != nil {
		return fmt.Errorf("failed to create indexes on walletLedger: %w", err)
	}
	paymentIndex := mongo.IndexModel{Keys: bson.D{{Key: "orgAddress", Value: 1}}}
	if _, err := database.Collection("processPayments").Indexes().CreateOne(ctx, paymentIndex); err != nil {
		return fmt.Errorf("failed to create indexes on processPayments: %w", err)
	}
	return nil
}

func downPaygBilling(context.Context, *mongo.Database) error {
	// no-op: dropping them would destroy payment data
	return nil
}
