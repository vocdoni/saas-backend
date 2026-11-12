package migrations

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func init() {
	AddMigration(36, "csp_attestations", upCSPAttestations, downCSPAttestations)
}

// upCSPAttestations creates cspAttestations, the attestation records of attested censuses keyed by
// the attested user id, and its (processId, _id) index, which serves the per-process count and the
// published page ordered by user id.
func upCSPAttestations(ctx context.Context, database *mongo.Database) error {
	if err := database.CreateCollection(ctx, "cspAttestations"); err != nil {
		// ignore "collection already exists" (code 48) so the migration is idempotent
		var cmdErr mongo.CommandError
		if !errors.As(err, &cmdErr) || cmdErr.Code != 48 {
			return fmt.Errorf("failed to create cspAttestations collection: %w", err)
		}
	}
	index := mongo.IndexModel{Keys: bson.D{{Key: "processId", Value: 1}, {Key: "_id", Value: 1}}}
	if _, err := database.Collection("cspAttestations").Indexes().CreateOne(ctx, index); err != nil {
		return fmt.Errorf("failed to create index on cspAttestations: %w", err)
	}
	return nil
}

func downCSPAttestations(context.Context, *mongo.Database) error {
	// no-op: dropping the collection would destroy the audit trail of finished processes
	return nil
}
