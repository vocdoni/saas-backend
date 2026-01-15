package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(9, "add_oauth_providers", upAddOAuthProviders, downAddOAuthProviders)
}

// upAddOAuthProviders adds the oauth field to the users collection
func upAddOAuthProviders(ctx context.Context, database *mongo.Database) error {
	users := database.Collection("users")

	// Idempotency check: see if oauth field already exists
	count, err := users.CountDocuments(ctx, bson.M{"oauth": bson.M{"$exists": true}})
	if err != nil {
		return fmt.Errorf("failed to check for existing oauth field: %w", err)
	}

	// If field already exists in any document, migration already applied
	if count > 0 {
		fmt.Println("oauth field already exists, skipping migration")
		return nil
	}

	// Add oauth field with empty map to all users
	result, err := users.UpdateMany(ctx,
		bson.M{}, // Match all documents
		bson.M{"$set": bson.M{"oauth": bson.M{}}},
	)
	if err != nil {
		return fmt.Errorf("failed to add oauth field: %w", err)
	}

	fmt.Printf("Added oauth field to %d user documents\n", result.ModifiedCount)
	return nil
}

// downAddOAuthProviders removes the oauth field from the users collection
// For backward compatibility, it migrates OAuth credentials back to the password field
func downAddOAuthProviders(ctx context.Context, database *mongo.Database) error {
	users := database.Collection("users")

	// First, find all users with OAuth credentials and migrate them back to password field
	cursor, err := users.Find(ctx, bson.M{
		"oauth":    bson.M{"$exists": true, "$ne": bson.M{}},
		"password": "",
	})
	if err != nil {
		return fmt.Errorf("failed to find OAuth users: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			fmt.Printf("Warning: failed to close cursor: %v\n", err)
		}
	}()

	migratedCount := 0
	for cursor.Next(ctx) {
		var user bson.M
		if err := cursor.Decode(&user); err != nil {
			return fmt.Errorf("failed to decode user: %w", err)
		}

		// Get the oauth map
		oauthMap, ok := user["oauth"].(bson.M)
		if !ok || len(oauthMap) == 0 {
			continue
		}

		// Get the first OAuth provider's signature hash
		// We iterate over the map to get the first provider (google/github/facebook)
		var signatureHash string
		for _, providerData := range oauthMap {
			if provider, ok := providerData.(bson.M); ok {
				if hash, ok := provider["signatureHash"].(string); ok {
					signatureHash = hash
					break
				}
			}
		}

		if signatureHash == "" {
			continue
		}

		// Migrate the OAuth signature hash back to password field
		userID := user["_id"]
		_, err := users.UpdateOne(ctx,
			bson.M{"_id": userID},
			bson.M{"$set": bson.M{"password": signatureHash}},
		)
		if err != nil {
			return fmt.Errorf("failed to migrate OAuth user %v back to password: %w", userID, err)
		}
		migratedCount++
	}

	if err := cursor.Err(); err != nil {
		return fmt.Errorf("cursor error: %w", err)
	}

	fmt.Printf("Migrated %d OAuth users back to password-based storage\n", migratedCount)

	// Now remove the oauth field from all users
	result, err := users.UpdateMany(ctx,
		bson.M{"oauth": bson.M{"$exists": true}},
		bson.M{"$unset": bson.M{"oauth": ""}},
	)
	if err != nil {
		return fmt.Errorf("failed to remove oauth field: %w", err)
	}

	fmt.Printf("Removed oauth field from %d user documents\n", result.ModifiedCount)
	return nil
}
