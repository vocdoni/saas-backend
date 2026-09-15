package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

// defaultLangAtMigration21 is the default notification language as it stood when this migration was
// written. It is deliberately a literal rather than apicommon.DefaultLang: a migration records what
// was written to the database at one point in time, so changing the service default later must not
// retroactively change what this stamped.
const defaultLangAtMigration21 = "en"

func init() {
	AddMigration(21, "backfill_organization_default_lang",
		upBackfillOrganizationDefaultLang, downBackfillOrganizationDefaultLang)
}

// upBackfillOrganizationDefaultLang stamps the default notification language on organizations that
// predate the `defaultLang` field (issue #675). Both creation paths assign one, so every
// organization created since carries a language; without this, older ones report an empty value
// over the API while new ones report "en", and a dashboard building a selector from
// GET /organizations/languages has nothing to preselect for them. The value is the one they already
// resolve to — NotificationLang falls back to the default when the organization has none — so this
// changes no notification, only the stored contract. Empty strings are backfilled alongside missing
// fields: the field is `omitempty`, so a stored "" is the same unset state written by an older
// binary rather than a deliberate choice.
func upBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	res, err := database.Collection("organizations").UpdateMany(ctx,
		// $in with null matches documents missing the field as well as stored empties
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

// downBackfillOrganizationDefaultLang removes the field from every organization. The schema this
// rolls back to has no `defaultLang` at all, so unsetting it everywhere — not just where the up
// stamped it — is what actually restores that state; a deliberate value cannot be told apart from a
// backfilled one anyway, and organizations keep behaving identically since a missing language
// resolves to the default.
func downBackfillOrganizationDefaultLang(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"defaultLang": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"defaultLang": ""}},
	); err != nil {
		return fmt.Errorf("failed to unset defaultLang on organizations: %w", err)
	}
	return nil
}
