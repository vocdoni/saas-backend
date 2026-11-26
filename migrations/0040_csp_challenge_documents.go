package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(40, "csp_challenge_documents", upCSPChallengeDocuments, downCSPChallengeDocuments)
}

// cspTokenIDIndexName names the unique index resolving a token to its challenge document, kept
// explicit so the down migration drops it deterministically.
const cspTokenIDIndexName = "csp_tokens_token_id"

// upCSPChallengeDocuments turns every cspTokens row into a challenge document: a row holding one
// code under its token gets that token as its only one, and a verified row is verified by it. The
// rows' verified field is left in place and ignored. Then it indexes the tokens: unique, so a token
// answers one code, and partial, so rows without tokens (inserted by an instance still running the
// previous code during a rolling deploy) never collide as nulls. Re-running it changes nothing.
func upCSPChallengeDocuments(ctx context.Context, database *mongo.Database) error {
	coll := database.Collection("cspTokens")
	if _, err := coll.UpdateMany(ctx,
		bson.M{"tokens": bson.M{"$exists": false}},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{
			"tokens": bson.A{bson.M{"id": "$_id", "createdat": "$createdat"}},
		}}}},
	); err != nil {
		return fmt.Errorf("failed to set csp challenge tokens: %w", err)
	}
	if _, err := coll.UpdateMany(ctx,
		bson.M{"verified": true, "verifiedby": bson.M{"$exists": false}},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{"verifiedby": "$_id"}}}},
	); err != nil {
		return fmt.Errorf("failed to set csp challenge verifiers: %w", err)
	}
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "tokens.id", Value: 1}},
		Options: options.Index().
			SetName(cspTokenIDIndexName).
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"tokens.id": bson.M{"$exists": true}}),
	}
	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create csp token id index: %w", err)
	}
	return nil
}

// downCSPChallengeDocuments drops the token index, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone. It is not lossless: the documents keep the
// new shape (random _id, no verified), so the previous code cannot resolve the tokens this code
// issued until they expire, one OTP TTL later.
func downCSPChallengeDocuments(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspTokens"), []string{cspTokenIDIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop csp token id index: %w", err)
	}
	return nil
}
