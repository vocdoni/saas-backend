package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson"
)

func init() {
	goose.AddMigrationContext(upAddUserPreferences, downAddUserPreferences)
}

func upAddUserPreferences(ctx context.Context, _ *sql.Tx) error {
	// Get MongoDB database connection
	database := db.GetMongoDB()

	// Get the users collection
	users := database.Collection("users")

	// Add default preferences to all existing users
	// This is an example of a data transformation migration
	filter := bson.M{"preferences": bson.M{"$exists": false}}
	update := bson.M{
		"$set": bson.M{
			"preferences": bson.M{
				"emailNotifications": true,
				"smsNotifications":   false,
				"language":           "en",
				"timezone":           "UTC",
			},
		},
	}

	result, err := users.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to add preferences to users: %w", err)
	}

	fmt.Printf("Added preferences to %d users\n", result.ModifiedCount)
	return nil
}

func downAddUserPreferences(ctx context.Context, _ *sql.Tx) error {
	// Get MongoDB database connection
	database := db.GetMongoDB()

	// Get the users collection
	users := database.Collection("users")

	// Remove preferences field from all users
	filter := bson.M{}
	update := bson.M{
		"$unset": bson.M{
			"preferences": "",
		},
	}

	result, err := users.UpdateMany(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to remove preferences from users: %w", err)
	}

	fmt.Printf("Removed preferences from %d users\n", result.ModifiedCount)
	return nil
}
