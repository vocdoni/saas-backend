package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(25, "rehash_login_hashes", upRehashLoginHashes, downRehashLoginHashes)
}

// upRehashLoginHashes rewrites every census participant login hash in the format of
// internal.HashLoginFields, which replaced the plaintext-plus-constant values of the old
// HashSortedFields. It is RepairLoginHashes applied: rows that would collide are skipped and
// reported, and orphans keep their old hashes, which match no login.
func upRehashLoginHashes(ctx context.Context, database *mongo.Database) error {
	report, err := RepairLoginHashes(ctx, database, RepairOptions{Apply: true})
	if err != nil {
		return fmt.Errorf("rehashing login hashes: %w", err)
	}
	log.Infow("rehashed census participant login hashes",
		"scanned", report.ParticipantsScanned, "rehashed", report.ParticipantsRehashed,
		"skipped", report.ParticipantsSkipped, "orphans", report.OrphanParticipants,
		"collisionGroups", len(report.CollisionGroups))
	return nil
}

// downRehashLoginHashes is a deliberate no-op: the new hashes are one-way, and the old format
// cannot be restored without bringing back the near-plaintext it embedded.
//
// WARNING: do not "undo" this migration by running a previous release's scripts/repairlogins
// --apply against a database served by the current code. That script writes the old hash
// format, which the current login path never computes, so it would lock out every voter it
// touches. Restoring old hashes is only meaningful together with rolling the service itself
// back to a release that computes them; otherwise leave the hashes as they are. Participants
// this migration skipped because of a collision are listed (member IDs only) in its log and by
// scripts/repairlogins, which can also unset their stale hashes with --unsetUnresolved.
func downRehashLoginHashes(_ context.Context, _ *mongo.Database) error {
	return nil
}
