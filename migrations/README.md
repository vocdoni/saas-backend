# MongoDB Migrations

This directory contains database migrations for the Vocdoni SaaS backend. Migrations are automatically run when the service starts up.

## Overview

The migration system uses [pressly/goose](https://github.com/pressly/goose) with MongoDB support. Migrations run automatically by default to ensure your database schema is always up-to-date with your application code.

## Migration Files

Migration files follow the naming convention: `YYYYMMDDHHMMSS_description.go`

Examples:
- `20240101000001_initial_indexes.go` - Creates all initial database indexes
- `20240102000001_add_user_preferences.go` - Adds user preferences field

## How Migrations Work

1. **Automatic Execution**: Migrations run automatically when the service starts
2. **Version Tracking**: Applied migrations are tracked in the `goose_db_version` collection
3. **Idempotent**: Migrations can be run multiple times safely
4. **Ordered**: Migrations run in chronological order based on timestamp

## Usage

### Normal Startup (Recommended)
```bash
# Runs migrations automatically, then starts the server
./service

# With environment variables
VOCDONI_MONGO_URL="mongodb://localhost:27017" ./service
```

### Migration-Only Mode
```bash
# Run migrations and exit (useful for CI/CD)
./service --migrate-only

# With custom migration directory
./service --migrate-only --migrations-dir=./custom-migrations
```

### Skip Migrations (Dangerous)
```bash
# Skip migrations entirely (not recommended for production)
./service --skip-migrations
```

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
- ✅ `20240103120000_add_user_email_verification.go`
- ❌ `20240103120000_update_users.go`

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

## Environment Variables

You can configure migrations using environment variables:

```bash
# Skip migrations (dangerous)
VOCDONI_SKIP_MIGRATIONS=true

# Run migrations only
VOCDONI_MIGRATE_ONLY=true

# Custom migration directory
VOCDONI_MIGRATIONS_DIR=./custom-migrations
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
db.goose_db_version.drop()
```

### View Migration Status
The migration system tracks applied migrations in the `goose_db_version` collection:

```javascript
// In MongoDB shell
db.goose_db_version.find().sort({id: 1})
```

## Production Deployment

### Recommended Deployment Process
1. **Deploy new code** with migrations
2. **Migrations run automatically** on service startup
3. **Service starts** after successful migrations

### Zero-Downtime Deployments
- Ensure migrations are backward-compatible
- Use feature flags for breaking changes
- Consider blue-green deployments for major schema changes

### Monitoring
- Monitor migration execution time
- Set up alerts for migration failures
- Log migration results for audit trails
