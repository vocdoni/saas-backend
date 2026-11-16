package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(37, "canonical_national_id_hashes", upCanonicalNationalIDHashes, downCanonicalNationalIDHashes)
}

// upCanonicalNationalIDHashes rehashes the census participants whose login hash includes a national
// ID that is a valid DNI or NIE written in a non-canonical form, now that hashMemberFields hashes it
// canonically (internal.CanonicalNationalID). It is RepairLoginHashes applied, as in migration 0025:
// rows of one census that would share a recomputed hash (a member stored twice under two spellings
// of one DNI) are all skipped and keep their old hashes. A login always hashes the canonical form, so
// of such twins only one stored in canonical form still logs in; the others cannot until the organiser
// removes the duplicate and scripts/repairlogins -apply rehashes the row that is left.
func upCanonicalNationalIDHashes(ctx context.Context, database *mongo.Database) error {
	report, err := RepairLoginHashes(ctx, database, RepairOptions{Apply: true})
	if err != nil {
		return fmt.Errorf("rehashing canonical national id login hashes: %w", err)
	}
	log.Infow("rehashed census participant login hashes with canonical national ids",
		"scanned", report.ParticipantsScanned, "rehashed", report.ParticipantsRehashed,
		"skipped", report.ParticipantsSkipped, "orphans", report.OrphanParticipants)
	return nil
}

// downCanonicalNationalIDHashes is a no-op: the previous hashes are recomputed by running the
// previous release's scripts/repairlogins with -apply against the database.
func downCanonicalNationalIDHashes(_ context.Context, _ *mongo.Database) error {
	return nil
}
