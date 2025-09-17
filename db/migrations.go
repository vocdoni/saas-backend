package db

import (
	"context"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// MigrationRecord represents a migration record stored in MongoDB
type MigrationRecord struct {
	Version   int       `bson:"version"`
	AppliedAt time.Time `bson:"applied_at"`
}

// RunMigrationsUp executes all pending database migrations
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
func getAppliedMigrations(ctx context.Context, collection *mongo.Collection) ([]MigrationRecord, error) {
	opts := options.Find().SetSort(bson.D{{Key: "version", Value: -1}})
	cursor, err := collection.Find(ctx, bson.M{}, opts)
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
