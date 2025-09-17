# MongoDB Migrations

This directory contains database migrations for the Vocdoni SaaS backend. Migrations are automatically run when the service starts up.

## Overview

The migration system uses embedded go funcs with MongoDB support. Migrations run automatically by default to ensure your database schema is always up-to-date with your application code.

## Migration Files

Migration files follow the naming convention: `NNNN_description.go`

Examples:
- `0001_initial_indexes.go` - Creates all initial database indexes
- `0002_something_new.go` - Adds something new

## How Migrations Work

1. **Automatic Execution**: Migrations run automatically when the service starts
2. **Version Tracking**: Applied migrations are tracked in the `migrations` collection
3. **Idempotent**: Migrations can be run multiple times safely
4. **Ordered**: Migrations run in version order

## Creating New Migrations

## Best Practices

### 1. Always Include Rollback Logic
Every migration should have a corresponding `down` function that can undo the changes.

### 2. Make Migrations Idempotent
Migrations should be safe to run multiple times:
```go
// Good: Check if field exists before adding
filter := bson.M{"newField": bson.M{"$exists": false}}

// Bad: Always add field (will cause errors on re-run)
filter := bson.M{}
```

### 3. Use Descriptive Names
- ✅ `0003_add_user_email_verification.go`
- ❌ `0003_update_users.go`

### 4. Test Migrations Thoroughly
- Test both up and down migrations
- Test with realistic data volumes
- Test migration rollbacks

### 5. Handle Errors Gracefully
```go
result, err := collection.UpdateMany(ctx, filter, update)
if err != nil {
	return fmt.Errorf("failed to update documents: %w", err)
}
log.Printf("Updated %d documents", result.ModifiedCount)
```

### 6. Example

```go
// file 0002_add_user_preferences.go

package migrations

import (
       "context"
       "fmt"

       "go.mongodb.org/mongo-driver/bson"
       "go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(2, "add_user_preferences", upAddUserPreferences, downAddUserPreferences)
}

func upAddUserPreferences(ctx context.Context, database *mongo.Database) error {
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

func downAddUserPreferences(ctx context.Context, database *mongo.Database) error {
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
```

## Troubleshooting

### Migration Fails
1. Check the error message in the logs
2. Verify your MongoDB connection
3. Ensure the migration syntax is correct
4. Test the migration logic manually

### Reset Migration State (Development Only)
```bash
# Connect to MongoDB and drop the version collection
mongo your-database
db.migrations.drop()
```

### View Migration Status
The migration system tracks applied migrations in the `migrations` collection:

```javascript
// In MongoDB shell
db.migrations.find().sort({id: 1})
```
