package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

// MigrationProvider is an interface for providing MongoDB database access to migrations
type MigrationProvider interface {
	GetDatabase() *mongo.Database
}

// Implement MigrationProvider for MongoStorage
func (ms *MongoStorage) GetDatabase() *mongo.Database {
	return ms.DBClient.Database(ms.database)
}

// RunMigrations executes all pending database migrations
func (ms *MongoStorage) RunMigrations(migrationDir string) error {
	log.Infow("starting database migrations", "dir", migrationDir)

	// Set up goose with a custom provider for MongoDB
	goose.SetBaseFS(nil)

	// Create a context with timeout for migrations
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// We need to use a SQL-like interface for goose, but we'll pass our MongoDB connection
	// through a custom provider. This is a bit of a hack, but it works with goose's architecture.

	// Create a dummy SQL DB connection for goose (it won't be used)
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return fmt.Errorf("failed to create dummy SQL connection: %w", err)
	}
	defer db.Close()

	// Set up goose with MongoDB-specific configuration
	goose.SetDialect("sqlite3") // We use sqlite3 as the dialect but override the operations

	// Store the MongoDB connection in a global variable that migrations can access
	currentMongoProvider = ms

	// Run the migrations
	if err := goose.UpContext(ctx, db, migrationDir); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Infow("database migrations completed successfully")
	return nil
}

// RunMigrationsDown rolls back database migrations
func (ms *MongoStorage) RunMigrationsDown(migrationDir string, steps int) error {
	log.Infow("rolling back database migrations", "dir", migrationDir, "steps", steps)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return fmt.Errorf("failed to create dummy SQL connection: %w", err)
	}
	defer db.Close()

	goose.SetDialect("sqlite3")
	currentMongoProvider = ms

	if steps > 0 {
		for i := 0; i < steps; i++ {
			if err := goose.DownContext(ctx, db, migrationDir); err != nil {
				return fmt.Errorf("failed to rollback migration step %d: %w", i+1, err)
			}
		}
	} else {
		if err := goose.DownContext(ctx, db, migrationDir); err != nil {
			return fmt.Errorf("failed to rollback migrations: %w", err)
		}
	}

	log.Infow("database migration rollback completed successfully")
	return nil
}

// GetMigrationStatus returns the current migration status
func (ms *MongoStorage) GetMigrationStatus(migrationDir string) error {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return fmt.Errorf("failed to create dummy SQL connection: %w", err)
	}
	defer db.Close()

	goose.SetDialect("sqlite3")
	currentMongoProvider = ms

	return goose.Status(db, migrationDir)
}

// Global variable to store the current MongoDB provider for migrations
// This is a workaround for goose's SQL-centric architecture
var currentMongoProvider MigrationProvider

// GetMongoDB returns the current MongoDB database for use in migrations
func GetMongoDB() *mongo.Database {
	if currentMongoProvider == nil {
		panic("MongoDB provider not set - migrations must be run through MongoStorage.RunMigrations()")
	}
	return currentMongoProvider.GetDatabase()
}
