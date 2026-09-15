package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

// defaultLangAtMigration21 is the default notification language as it stood when this migration ran.
// A literal on purpose, not apicommon.DefaultLang: changing the service default later must not
// change what this wrote.
const defaultLangAtMigration21 = "en"

func init() {
	AddMigration(21, "backfill_organization_default_lang",
		upBackfillOrganizationDefaultLang, downBackfillOrganizationDefaultLang)
}

// upBackfillOrganizationDefaultLang stamps the default notification language on organizations that
// predate the `defaultLang` field (issue #675), so it reads the same on every organization instead
// of empty on the older ones. It changes no notification: those organizations already resolved to
// the default, since NotificationLang falls back to it when the organization has none.
func upBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	res, err := database.Collection("organizations").UpdateMany(ctx,
		// the field is omitempty, so a stored "" is the same unset state as a missing key,
		// and $in with null matches both
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
// Behaviour is unchanged either way, a missing language resolving to the default.
func downBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"defaultLang": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"defaultLang": ""}},
	); err != nil {
		return fmt.Errorf("failed to unset defaultLang on organizations: %w", err)
	}
	return nil
}
