package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(24, "rehash_login_hashes", upRehashLoginHashes, downRehashLoginHashes)
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
		"skipped", report.ParticipantsSkipped, "orphans", report.OrphanParticipants)
	return nil
}

// downRehashLoginHashes is a no-op: the old hashes are recomputed by running the previous
// release's scripts/repairlogins with -apply against the database.
func downRehashLoginHashes(_ context.Context, _ *mongo.Database) error {
	return nil
}
