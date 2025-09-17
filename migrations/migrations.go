// Package migrations handles MongoDB migrations
package migrations

import (
	"context"
	"maps"
	"sort"

	"go.mongodb.org/mongo-driver/mongo"
)

// MigrationFunc represents a migration function
type MigrationFunc func(ctx context.Context, database *mongo.Database) error

// Migration represents a single migration
type Migration struct {
	Version int
	Name    string
	Up      MigrationFunc
	Down    MigrationFunc
}

// Global registry for migrations
var migrationRegistry = make(map[int]Migration)

// AddMigration registers a migration in the global registry
func AddMigration(version int, name string, up, down MigrationFunc) {
	migrationRegistry[version] = Migration{
		Version: version,
		Name:    name,
		Up:      up,
		Down:    down,
	}
}

// DelMigration deregisters a migration in the global registry
func DelMigration(version int) { delete(migrationRegistry, version) }

// SortedByVersionAsc returns all registered migrations, sorted by ascending version
func SortedByVersionAsc() []Migration {
	var migs []Migration
	for _, mig := range migrationRegistry {
		migs = append(migs, mig)
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs
}

// AsMap returns all migrations as a map
func AsMap() map[int]Migration {
	return maps.Clone(migrationRegistry)
}
