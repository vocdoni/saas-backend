package migrations

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

// signerAddressAtMigration28 re-derives an organization signer address exactly as
// account.OrganizationSigner did when this migration was written. A local copy on purpose:
// migrations cannot import the account package (db imports migrations), and the check must
// keep reproducing the historical derivation even if the account package changes later.
func signerAddressAtMigration28(secret, seed, nonce string) (common.Address, error) {
	key := hex.EncodeToString(ethereum.HashRaw([]byte(secret + seed + nonce)))
	signer := ethereum.SignKeys{}
	if err := signer.AddHexKey(key); err != nil {
		return common.Address{}, err
	}
	return signer.Address(), nil
}

func init() {
	AddMigration(29, "organization_signer_seed", upOrganizationSignerSeed, downOrganizationSignerSeed)
}

// upOrganizationSignerSeed stamps every organization with an immutable `signerSeed`, copied from
// its current `creator`. The organization signer key has always been derived from
// secret+creator+nonce, but `creator` is rewritten when the creator changes their email, which
// silently re-derives a different key and strands the organization's on-chain account. From this
// migration on the derivation reads `signerSeed`, which is written once at creation and never
// updated.
//
// When the service secret is available (VOCDONI_SECRET), the migration also re-derives each
// organization's address from secret+signerSeed+nonce and logs the ones that do not match their
// stored address — those organizations already lost their key to a creator email change (or were
// created under a different secret) and need manual attention. Mismatches are logged, not fixed:
// the original email is unknown.
func upOrganizationSignerSeed(ctx context.Context, database *mongo.Database) error {
	orgs := database.Collection("organizations")
	res, err := orgs.UpdateMany(ctx,
		bson.M{
			// the field is omitempty, so $in with null to match a missing key as well as a stored ""
			"signerSeed": bson.M{"$in": bson.A{nil, ""}},
			"creator":    bson.M{"$nin": bson.A{nil, ""}},
		},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{"signerSeed": "$creator"}}}},
	)
	if err != nil {
		return fmt.Errorf("failed to backfill signerSeed on organizations: %w", err)
	}
	log.Infow("backfilled organization signer seed from creator", "organizations", res.ModifiedCount)

	secret := os.Getenv("VOCDONI_SECRET")
	if secret == "" {
		log.Warnw("VOCDONI_SECRET not set: skipping organization signer address verification")
		return nil
	}
	cursor, err := orgs.Find(ctx, bson.M{},
		options.Find().SetProjection(bson.M{"_id": 1, "signerSeed": 1, "nonce": 1}))
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	mismatches := 0
	for cursor.Next(ctx) {
		var org struct {
			RawAddress bson.RawValue `bson:"_id"`
			SignerSeed string        `bson:"signerSeed"`
			Nonce      string        `bson:"nonce"`
		}
		if err := cursor.Decode(&org); err != nil {
			return fmt.Errorf("failed to decode organization: %w", err)
		}
		var address common.Address
		if err := org.RawAddress.Unmarshal(&address); err != nil {
			// a row whose _id is not an address (should not happen) cannot be verified
			log.Warnw("organization _id is not an address, skipping signer verification",
				"id", org.RawAddress.String(), "error", err)
			continue
		}
		if org.SignerSeed == "" {
			// no creator to derive from (should not happen); nothing to verify
			continue
		}
		derived, err := signerAddressAtMigration28(secret, org.SignerSeed, org.Nonce)
		if err != nil {
			return fmt.Errorf("failed to derive signer for organization %s: %w", address, err)
		}
		if derived != address {
			mismatches++
			log.Warnw("organization signer seed does not derive its stored address: "+
				"its key was already lost (creator email change or different secret)",
				"org", address, "derived", derived)
		}
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("failed to iterate organizations: %w", err)
	}
	if mismatches > 0 {
		log.Warnw("organizations with non-matching signer derivation", "count", mismatches)
	}
	return nil
}

// downOrganizationSignerSeed removes the field from every organization: the schema this rolls
// back to derives the signer from `creator` again, which the up migration copied verbatim.
func downOrganizationSignerSeed(ctx context.Context, database *mongo.Database) error {
	if _, err := database.Collection("organizations").UpdateMany(ctx,
		bson.M{"signerSeed": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"signerSeed": ""}},
	); err != nil {
		return fmt.Errorf("failed to unset signerSeed on organizations: %w", err)
	}
	return nil
}
