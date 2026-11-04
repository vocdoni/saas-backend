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
	AddMigration(35, "payg_billing", upPaygBilling, downPaygBilling)
}

// upPaygBilling creates processPayments (keyed by process id), wallets (keyed by payer) and
// walletLedger, whose unique (orgAddress, idempotencyKey) index stops a replay being recorded
// twice in a wallet — keys are per wallet, as the balance applies them — whose paymentIntentId
// index finds the top-up a Stripe refund returns, and whose (orgAddress, processId) index serves
// the per-process fold of what a process paid.
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
			Keys:    bson.D{{Key: "orgAddress", Value: 1}, {Key: "idempotencyKey", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		// the ledger page sorts by createdAt then _id (tie-break), so both are in the index or every
		// page is an in-memory sort over the organization's whole ledger
		{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}},
		// a Stripe refund names the payment intent of the top-up it returns
		{Keys: bson.D{{Key: "paymentIntentId", Value: 1}}, Options: options.Index().SetSparse(true)},
		// what a process paid is folded from its payer's rows of it (ProcessLedgerState); only
		// payment and creditBack rows name a process
		{
			Keys:    bson.D{{Key: "orgAddress", Value: 1}, {Key: "processId", Value: 1}},
			Options: options.Index().SetPartialFilterExpression(bson.M{"processId": bson.M{"$exists": true}}),
		},
	}
	if _, err := database.Collection("walletLedger").Indexes().CreateMany(ctx, ledgerIndexes); err != nil {
		return fmt.Errorf("failed to create indexes on walletLedger: %w", err)
	}
	return nil
}

func downPaygBilling(context.Context, *mongo.Database) error {
	// no-op: dropping them would destroy payment data
	return nil
}
