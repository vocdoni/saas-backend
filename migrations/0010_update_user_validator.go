package migrations

import (
	"context"
	"fmt"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(10, "update_user_validator", upUpdateUserValidator, downUpdateUserValidator)
}

// upUpdateUserValidator updates the users collection validator to allow empty passwords for OAuth-only users
func upUpdateUserValidator(ctx context.Context, database *mongo.Database) error {
	// New validator that allows empty passwords (for OAuth-only users)
	newValidator := bson.M{
		"$jsonSchema": bson.M{
			"bsonType": "object",
			"required": []string{"_id", "email", "password"},
			"properties": bson.M{
				"id": bson.M{
					"bsonType":    "int",
					"description": "must be an integer and is required",
					"minimum":     1,
				},
				"email": bson.M{
					"bsonType":    "string",
					"description": "must be an email and is required",
					"pattern":     internal.EmailRegexTemplate,
				},
				"password": bson.M{
					"bsonType":    "string",
					"description": "must be a string (empty string allowed for OAuth-only users)",
					// Removed minLength: 8 to allow empty passwords for OAuth users
				},
			},
		},
	}

	// Update the collection with the new validator
	command := bson.D{
		{Key: "collMod", Value: "users"},
		{Key: "validator", Value: newValidator},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}

	if err := database.RunCommand(ctx, command).Err(); err != nil {
		return fmt.Errorf("failed to update users collection validator: %w", err)
	}

	fmt.Println("Updated users collection validator to allow empty passwords for OAuth users")
	return nil
}

// downUpdateUserValidator reverts the users collection validator to the original version
func downUpdateUserValidator(ctx context.Context, database *mongo.Database) error {
	// Original validator with minLength: 8 for passwords
	originalValidator := bson.M{
		"$jsonSchema": bson.M{
			"bsonType": "object",
			"required": []string{"_id", "email", "password"},
			"properties": bson.M{
				"id": bson.M{
					"bsonType":    "int",
					"description": "must be an integer and is required",
					"minimum":     1,
				},
				"email": bson.M{
					"bsonType":    "string",
					"description": "must be an email and is required",
					"pattern":     internal.EmailRegexTemplate,
				},
				"password": bson.M{
					"bsonType":    "string",
					"description": "must be a string and is required",
					"minLength":   8,
				},
			},
		},
	}

	// Update the collection with the original validator
	command := bson.D{
		{Key: "collMod", Value: "users"},
		{Key: "validator", Value: originalValidator},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}

	if err := database.RunCommand(ctx, command).Err(); err != nil {
		return fmt.Errorf("failed to revert users collection validator: %w", err)
	}

	fmt.Println("Reverted users collection validator to require 8-character passwords")
	return nil
}
