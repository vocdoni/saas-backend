package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(26, "clear_legacy_member_passwords", upClearLegacyMemberPasswords, downClearLegacyMemberPasswords)
}

// upClearLegacyMemberPasswords unsets the stored password (`pass`) of every organization member.
// Values stored before members' passwords became a voter credential are the argon2 hash of a
// plaintext, never of the client hash the CSP now compares (nor salted with the member id), so they
// can never match: such a member would report hasPassword, be admitted to password censuses and then
// always fail to log in, looking to the voter like someone outside the census. Cleared, those
// members report missing data instead, so organizers see they need a new password. Re-running it
// only clears passwords set since, so it must not be re-applied by hand after the release.
func upClearLegacyMemberPasswords(ctx context.Context, database *mongo.Database) error {
	result, err := database.Collection("orgMembers").UpdateMany(ctx,
		bson.M{"pass": bson.M{"$exists": true}}, bson.M{"$unset": bson.M{"pass": ""}})
	if err != nil {
		return fmt.Errorf("failed to clear legacy member passwords: %w", err)
	}
	log.Infow("cleared legacy member passwords", "members", result.ModifiedCount)
	return nil
}

// downClearLegacyMemberPasswords is a no-op: the cleared hashes cannot be recovered, and they were
// unusable anyway, since no login could ever match them.
func downClearLegacyMemberPasswords(context.Context, *mongo.Database) error {
	return nil
}
