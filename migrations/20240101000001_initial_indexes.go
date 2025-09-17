package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	goose.AddMigrationContext(upInitialIndexes, downInitialIndexes)
}

func upInitialIndexes(ctx context.Context, _ *sql.Tx) error {
	// Get MongoDB database connection
	database := db.GetMongoDB()

	// Get collections
	users := database.Collection("users")
	verifications := database.Collection("verifications")
	organizationInvites := database.Collection("organizationInvites")
	orgMembers := database.Collection("orgMembers")
	censusParticipants := database.Collection("censusParticipants")
	cspTokensStatus := database.Collection("cspTokensStatus")
	jobs := database.Collection("jobs")

	// Create an index for the 'email' field on users
	if _, err := users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on email for users: %w", err)
	}

	// Create an index for the ('code', 'type') tuple on user verifications (must be unique)
	if _, err := verifications.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "code", Value: 1},
			{Key: "type", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on code and type for verifications: %w", err)
	}

	// Create indexes for organization invites
	if _, err := organizationInvites.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// Create an index for the 'invitationCode' field on organization invites (must be unique)
		{
			Keys:    bson.D{{Key: "invitationCode", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetUnique(true),
		},
		// Create a ttl index for the 'expiration' field on organization invites
		{
			Keys:    bson.D{{Key: "expiration", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetExpireAfterSeconds(0),
		},
		// Create an index to ensure that the tuple ('organizationAddress', 'newUserEmail') is unique
		{
			Keys: bson.D{
				{Key: "organizationAddress", Value: 1},
				{Key: "newUserEmail", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
	}); err != nil {
		return fmt.Errorf("failed to create many indexes for organization invites: %w", err)
	}

	// Create indexes for organization members
	if _, err := orgMembers.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// Index for the id field on organization members
		{
			Keys: bson.D{
				{Key: "_id", Value: 1},
			},
		},
		// Index for the orgAddress/id field on organization members
		{
			Keys: bson.D{
				{Key: "orgAddress", Value: 1},
				{Key: "_id", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		// Index for the tuple orgAddress and memberNumber
		{
			Keys: bson.D{
				{Key: "orgAddress", Value: 1},
				{Key: "memberNumber", Value: 1},
			},
		},
		// Index for the tuple orgAddress and email on organization members
		{
			Keys: bson.D{
				{Key: "orgAddress", Value: 1},
				{Key: "email", Value: 1},
			},
		},
		// Index for the tuple orgAddress and hashedPhone on organization members
		{
			Keys: bson.D{
				{Key: "orgAddress", Value: 1},
				{Key: "hashedPhone", Value: 1},
			},
		},
		// Member properties text index for filtering
		{
			Keys: bson.D{
				{Key: "firstName", Value: "text"},
				{Key: "lastName", Value: "text"},
				{Key: "email", Value: "text"},
				{Key: "phone", Value: "text"},
			},
		},
		// Individual indexes for regex search optimization on orgMembers
		{Keys: bson.D{{Key: "orgAddress", Value: 1}}},
		{Keys: bson.D{{Key: "firstName", Value: 1}}},
		{Keys: bson.D{{Key: "lastName", Value: 1}}},
		{Keys: bson.D{{Key: "email", Value: 1}}},
		{Keys: bson.D{{Key: "phone", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes for orgMembers: %w", err)
	}

	// Create indexes for census participants
	if _, err := censusParticipants.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// Index for the censusId
		{
			Keys: bson.D{
				{Key: "censusId", Value: 1},
			},
		},
		// Index for the participantID
		{
			Keys: bson.D{
				{Key: "participantID", Value: 1},
			},
		},
		// Index for the censusId and participantID tuple
		{
			Keys: bson.D{
				{Key: "censusId", Value: 1},
				{Key: "participantID", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
		// Index for the censusId and loginhash
		{
			Keys: bson.D{
				{Key: "censusId", Value: 1},
				{Key: "loginHash", Value: 1},
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to create indexes for censusParticipants: %w", err)
	}

	// Unique index over userID and processID for cspTokensStatus
	if _, err := cspTokensStatus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "processid", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on userid and processid for cspTokensStatus: %w", err)
	}

	// Create indexes for jobs
	if _, err := jobs.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// Create an index for the jobId field on jobs (must be unique)
		{
			Keys:    bson.D{{Key: "jobId", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetUnique(true),
		},
		// Create an index for the orgAddress field on jobs
		{
			Keys: bson.D{{Key: "orgAddress", Value: 1}}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create indexes for jobs: %w", err)
	}

	return nil
}

func downInitialIndexes(ctx context.Context, _ *sql.Tx) error {
	// Get MongoDB database connection
	database := db.GetMongoDB()

	// Drop all indexes from all collections
	for _, collName := range []string{
		"users",
		"verifications",
		"organizationInvites",
		"orgMembers",
		"censusParticipants",
		"cspTokensStatus",
		"jobs",
	} {
		collection := database.Collection(collName)
		if _, err := collection.Indexes().DropAll(ctx); err != nil {
			return fmt.Errorf("failed to drop indexes for collection %s: %w", collName, err)
		}
	}

	return nil
}
