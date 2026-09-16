package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(22, "payg_billing", upPaygBilling, downPaygBilling)
}

// upPaygBilling creates the pay-per-process billing collections:
//
//   - processPayments: payment state per voting process, keyed by the process id
//     (deliberately outside votingProcesses, whose write paths replace whole documents).
//   - wallets: integrator prepaid balances, keyed by organization address.
//   - walletLedger: the append-only audit trail of wallet credits and debits. The unique
//     index on idempotencyKey is what makes a replayed webhook or publish retry unable to
//     record the same operation twice.
func upPaygBilling(ctx context.Context, database *mongo.Database) error {
	for _, name := range []string{"processPayments", "wallets", "walletLedger"} {
		if err := database.CreateCollection(ctx, name); err != nil {
			// ignore "collection already exists" (code 48) so the migration is idempotent
			if cmdErr, ok := err.(mongo.CommandError); !ok || cmdErr.Code != 48 {
				return fmt.Errorf("failed to create %s collection: %w", name, err)
			}
		}
	}
	if _, err := database.Collection("walletLedger").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "idempotencyKey", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "createdAt", Value: -1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on walletLedger: %w", err)
	}
	if _, err := database.Collection("processPayments").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "orgAddress", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on processPayments: %w", err)
	}
	return nil
}

func downPaygBilling(context.Context, *mongo.Database) error {
	// Dropping these collections would destroy payment and balance data; matching the
	// repo policy for data-bearing collections we do nothing here. upPaygBilling is
	// idempotent.
	return nil
}
