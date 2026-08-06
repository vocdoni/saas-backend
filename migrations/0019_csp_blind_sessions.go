package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(19, "csp_blind_sessions", upCSPBlindSessions, downCSPBlindSessions)
}

// blindSessionTTLIndexName is the default name Mongo assigns to a single-field
// ascending index on expiresat; kept explicit so the down migration can drop it
// deterministically.
const blindSessionTTLIndexName = "expiresat_1"

// upCSPBlindSessions creates the cspBlindSessions collection, which holds the
// ephemeral secret k of an in-flight anonymous-signature session between the
// prepare and sign requests.
//
// The TTL index on expiresat is the reason this is a collection rather than an
// in-process map: a voter who prepares a signature and then walks away would
// otherwise leak a secret per abandoned attempt, and prepare and sign need not
// land on the same replica. expireAfterSeconds=0 means "delete once expiresat is
// in the past", letting each document carry its own deadline.
func upCSPBlindSessions(ctx context.Context, database *mongo.Database) error {
	if err := database.CreateCollection(ctx, "cspBlindSessions"); err != nil {
		// ignore "collection already exists" (code 48) so the migration is idempotent
		if cmdErr, ok := err.(mongo.CommandError); !ok || cmdErr.Code != 48 {
			return fmt.Errorf("failed to create cspBlindSessions collection: %w", err)
		}
	}
	if _, err := database.Collection("cspBlindSessions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "expiresat", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(0),
		},
		// used by the per-election cleanup on teardown
		{Keys: bson.D{{Key: "electionid", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on cspBlindSessions: %w", err)
	}
	return nil
}

// downCSPBlindSessions drops the TTL index. The collection itself is left in
// place: it only ever holds short-lived sessions that the TTL will drain anyway,
// and dropping collections is not this repo's rollback policy.
func downCSPBlindSessions(ctx context.Context, database *mongo.Database) error {
	coll := database.Collection("cspBlindSessions")
	if _, err := coll.Indexes().DropOne(ctx, blindSessionTTLIndexName); err != nil {
		return fmt.Errorf("failed to drop TTL index on cspBlindSessions: %w", err)
	}
	return nil
}
