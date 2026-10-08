package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// MigrationRecord represents a migration record stored in MongoDB
type MigrationRecord struct {
	Version   int       `bson:"version"`
	AppliedAt time.Time `bson:"applied_at"`
}

const (
	// migrationLockID is the _id of the cross-instance lock document in the
	// migrations collection.
	migrationLockID = "migration-lock"
	// migrationLockTTL is how long a lock left behind by a crashed instance
	// stays valid before another instance may take it over. It matches the
	// migration context timeout, so a live holder can never outlive its lock.
	migrationLockTTL = 10 * time.Minute
	// migrationLockRetryInterval is how long an instance waits between
	// attempts to take the lock while another instance holds it.
	migrationLockRetryInterval = 2 * time.Second
)

// acquireMigrationLock takes a cross-instance lock document in the migrations
// collection so only one service replica runs migrations at a time; the others
// wait until the lock is released or expires (crashed holder), or their
// context runs out.
func (ms *MongoStorage) acquireMigrationLock(ctx context.Context, holder string) error {
	for {
		now := time.Now()
		// take the lock if it does not exist (upsert) or has expired; a held
		// lock fails the filter and, because the filter pins _id, the upsert
		// then hits the unique _id and returns a duplicate-key error
		filter := bson.M{"_id": migrationLockID, "expiresat": bson.M{"$lt": now}}
		update := bson.M{"$set": bson.M{"holder": holder, "expiresat": now.Add(migrationLockTTL)}}
		_, err := ms.migrations.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
		if err == nil {
			return nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("failed to acquire migration lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gave up waiting for the migration lock: %w", ctx.Err())
		case <-time.After(migrationLockRetryInterval):
		}
	}
}

// releaseMigrationLock releases the cross-instance migration lock, but only if
// this instance still holds it (an expired lock may have been taken over).
func (ms *MongoStorage) releaseMigrationLock(holder string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.migrations.DeleteOne(ctx, bson.M{"_id": migrationLockID, "holder": holder}); err != nil {
		log.Warnw("failed to release migration lock", "error", err)
	}
}

// RunMigrationsUp executes all pending database migrations. A cross-instance
// lock makes it safe to start several service replicas at once: one applies
// the pending migrations while the others wait, re-check and find nothing left
// to do.
func (ms *MongoStorage) RunMigrationsUp() error {
	// Create a context with timeout for migrations
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	lastMigration, err := lastAppliedMigration(ctx, ms.migrations)
	if err != nil {
		return fmt.Errorf("failed to get last applied migration: %w", err)
	}

	migs := migrations.SortedByVersionAsc()

	if migs[len(migs)-1].Version == lastMigration {
		log.Infow("database is up-to-date, no need to migrate")
		return nil
	}

	// there is work to do: take the cross-instance lock so two replicas cannot
	// apply migrations concurrently
	holderBytes := make([]byte, 16)
	if _, err := rand.Read(holderBytes); err != nil {
		return fmt.Errorf("failed to generate migration lock holder id: %w", err)
	}
	holder := hex.EncodeToString(holderBytes)
	if err := ms.acquireMigrationLock(ctx, holder); err != nil {
		return err
	}
	defer ms.releaseMigrationLock(holder)

	// re-read under the lock: another instance may have migrated while we waited
	lastMigration, err = lastAppliedMigration(ctx, ms.migrations)
	if err != nil {
		return fmt.Errorf("failed to get last applied migration: %w", err)
	}
	if migs[len(migs)-1].Version == lastMigration {
		log.Infow("database is up-to-date, no need to migrate")
		return nil
	}

	log.Infow("starting database migrations", "migrationsAvailable", len(migs), "lastAppliedMigration", lastMigration)

	// Apply pending migrations
	for _, migration := range migs {
		if migration.Version <= lastMigration {
			continue
		}

		log.Infow("applying migration", "version", migration.Version, "name", migration.Name)

		if err := migration.Up(ctx, ms.DBClient.Database(ms.database)); err != nil {
			return fmt.Errorf("failed to apply migration %d (%s): %w", migration.Version, migration.Name, err)
		}

		record := MigrationRecord{
			Version:   migration.Version,
			AppliedAt: time.Now(),
		}
		if _, err := ms.migrations.InsertOne(ctx, record); err != nil {
			return fmt.Errorf("failed to record migration %d: %w", migration.Version, err)
		}

		log.Infow("migration applied successfully", "version", migration.Version, "name", migration.Name)
	}

	log.Infow("database migrations completed successfully")
	return nil
}

// RunMigrationsDown rolls back database migrations
func (ms *MongoStorage) RunMigrationsDown(steps int) error {
	log.Infow("rolling back database migrations", "steps", steps)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	lastMigration, err := lastAppliedMigration(ctx, ms.migrations)
	if err != nil {
		return fmt.Errorf("failed to get last applied migration: %w", err)
	}

	// Determine how many migrations to rollback
	if steps <= 0 || steps > lastMigration {
		steps = lastMigration
	}

	// Rollback migrations
	for version := lastMigration; version > lastMigration-steps; version-- {
		migrationRegistry := migrations.AsMap()
		migration, exists := migrationRegistry[version]
		if !exists {
			return fmt.Errorf("migration %d not found in registry", version)
		}

		log.Infow("rolling back migration", "version", migration.Version, "name", migration.Name)

		// Execute the rollback
		if err := migration.Down(ctx, ms.DBClient.Database(ms.database)); err != nil {
			return fmt.Errorf("failed to rollback migration %d (%s): %w", migration.Version, migration.Name, err)
		}

		// Remove the migration record
		filter := bson.M{"version": version}
		if _, err := ms.migrations.DeleteOne(ctx, filter); err != nil {
			return fmt.Errorf("failed to remove migration record %d: %w", version, err)
		}

		log.Infow("migration rolled back successfully", "version", migration.Version, "name", migration.Name)
	}

	log.Infow("database migration rollback completed successfully")
	return nil
}

// lastAppliedMigration returns the last applied migration version.
func lastAppliedMigration(ctx context.Context, collection *mongo.Collection) (int, error) {
	migs, err := getAppliedMigrations(ctx, collection)
	if err != nil {
		return 0, err
	}
	if len(migs) == 0 {
		return 0, nil
	}
	return migs[0].Version, nil
}

// getAppliedMigrations returns applied migration versions in descending order.
// The filter keeps the cross-instance lock document, which carries no version,
// out of the records.
func getAppliedMigrations(ctx context.Context, collection *mongo.Collection) ([]MigrationRecord, error) {
	opts := options.Find().SetSort(bson.D{{Key: "version", Value: -1}})
	cursor, err := collection.Find(ctx, bson.M{"version": bson.M{"$exists": true}}, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var migs []MigrationRecord
	if err = cursor.All(ctx, &migs); err != nil {
		return nil, fmt.Errorf("failed to decode migrations: %w", err)
	}

	return migs, cursor.Err()
}
