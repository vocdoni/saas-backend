package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

// defaultLangAtMigration21 is a literal on purpose, not apicommon.DefaultLang: changing the
// service default later must not change what this wrote.
const defaultLangAtMigration21 = "en"

func init() {
	AddMigration(21, "backfill_organization_default_lang",
		upBackfillOrganizationDefaultLang, downBackfillOrganizationDefaultLang)
}

// upBackfillOrganizationDefaultLang stamps the default language on organizations predating the
// `defaultLang` field (issue #675), which already resolved to it. Contract only, no mail changes.
func upBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	res, err := database.Collection("organizations").UpdateMany(ctx,
		// the field is omitempty, so $in with null to match a missing key as well as a stored ""
		bson.M{"defaultLang": bson.M{"$in": bson.A{nil, ""}}},
		bson.M{"$set": bson.M{"defaultLang": defaultLangAtMigration21}},
	)
	if err != nil {
		return fmt.Errorf("failed to backfill defaultLang on organizations: %w", err)
	}
	log.Infow("backfilled organization default language",
		"organizations", res.ModifiedCount, "lang", defaultLangAtMigration21)
	return nil
}

// downBackfillOrganizationDefaultLang removes the field from every organization: the schema this
// rolls back to has none, and a deliberate value cannot be told apart from a backfilled one.
func downBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"defaultLang": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"defaultLang": ""}},
	); err != nil {
		return fmt.Errorf("failed to unset defaultLang on organizations: %w", err)
	}
	return nil
}
