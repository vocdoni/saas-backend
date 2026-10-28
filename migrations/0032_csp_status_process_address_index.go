package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(32, "csp_status_process_address_index", upCSPStatusProcessAddressIndex, downCSPStatusProcessAddressIndex)
}

// cspStatusProcessAddressIndexName names the partial index on the CSP process rows that consumed
// an address, kept explicit so the down migration drops it deterministically.
const cspStatusProcessAddressIndexName = "csp_status_process_address"

// upCSPStatusProcessAddressIndex indexes the CSP process rows by election and consumed address. A
// relayed vote knows only those two, and caches the vote as on chain on the row they name; without
// the index every relay would scan cspTokensStatus. The index is partial: blind-census rows carry
// no address and are never looked up this way.
func upCSPStatusProcessAddressIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "processid", Value: 1}, {Key: "consumedaddress", Value: 1}},
		Options: options.Index().
			SetName(cspStatusProcessAddressIndexName).
			SetPartialFilterExpression(bson.M{"consumedaddress": bson.M{"$exists": true}}),
	}
	if _, err := database.Collection("cspTokensStatus").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create csp status process/address index: %w", err)
	}
	return nil
}

// downCSPStatusProcessAddressIndex drops the index, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone: rolling back twice must not fail.
func downCSPStatusProcessAddressIndex(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspTokensStatus"),
		[]string{cspStatusProcessAddressIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop csp status process/address index: %w", err)
	}
	return nil
}
