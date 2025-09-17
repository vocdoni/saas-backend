package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(2, "initial_indexes", upInitialIndexes, downInitialIndexes)
}

func upInitialIndexes(ctx context.Context, database *mongo.Database) error {
	ms := struct {
		users               *mongo.Collection
		verifications       *mongo.Collection
		organizationInvites *mongo.Collection
		orgMembers          *mongo.Collection
		censusParticipants  *mongo.Collection
		cspTokensStatus     *mongo.Collection
		jobs                *mongo.Collection
	}{
		users:               database.Collection("users"),
		verifications:       database.Collection("verifications"),
		organizationInvites: database.Collection("organizationInvites"),
		orgMembers:          database.Collection("orgMembers"),
		censusParticipants:  database.Collection("censusParticipants"),
		cspTokensStatus:     database.Collection("cspTokensStatus"),
		jobs:                database.Collection("jobs"),
	}

	// create an index for the 'email' field on users
	if _, err := ms.users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on email for users: %w", err)
	}

	// create an index for the ('code', 'type') tuple on user verifications (must be unique)
	if _, err := ms.verifications.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "code", Value: 1}, // 1 for ascending order
			{Key: "type", Value: 1}, // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on code and type for verifications: %w", err)
	}

	if _, err := ms.organizationInvites.Indexes().CreateMany(ctx, []mongo.IndexModel{
		// create an index for the 'invitationCode' field on organization invites (must be unique)
		{
			Keys:    bson.D{{Key: "invitationCode", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetUnique(true),
		},
		// create a ttl index for the 'expiration' field on organization invites
		{
			Keys:    bson.D{{Key: "expiration", Value: 1}}, // 1 for ascending order
			Options: options.Index().SetExpireAfterSeconds(0),
		},
		// create an index to ensure that the tuple ('organizationAddress', 'newUserEmail') is unique
		{
			Keys: bson.D{
				{Key: "organizationAddress", Value: 1}, // 1 for ascending order
				{Key: "newUserEmail", Value: 1},        // 1 for ascending order
			},
			Options: options.Index().SetUnique(true),
		},
	}); err != nil {
		return fmt.Errorf("failed to create many indexes for organization invites: %w", err)
	}

	// create an index for the orgAddress/id field on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
			{Key: "_id", Value: 1},        // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and id for orgMembers: %w", err)
	}

	// create an index for the orgAddress
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and memberNumber
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1},   // 1 for ascending order
			{Key: "memberNumber", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and memberNumber for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and email on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1}, // 1 for ascending order
			{Key: "email", Value: 1},      // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and email for orgMembers: %w", err)
	}

	// create an index for the tuple orgAddress and hashedPhone on organization members
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "orgAddress", Value: 1},  // 1 for ascending order
			{Key: "hashedPhone", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress and hashedPhone for orgMembers: %w", err)
	}

	// index for the censusId
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId for censusParticipants: %w", err)
	}

	// index for the participantID
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "participantID", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on participantID for censusParticipants: %w", err)
	}

	// index for the censusId and participantID tuple
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1},      // 1 for ascending order
			{Key: "participantID", Value: 1}, // 1 for ascending order
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId and participantID for censusParticipants: %w", err)
	}

	// index for the censusId and loginhash
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1},  // 1 for ascending order
			{Key: "loginHash", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId and loginHash for censusParticipants: %w", err)
	}
	// index for the censusId and loginhashPhone
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1},       // 1 for ascending order
			{Key: "loginHashPhone", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId and loginHashPhone for censusParticipants: %w", err)
	}
	// index for the censusId and loginhashEmail
	if _, err := ms.censusParticipants.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "censusId", Value: 1},       // 1 for ascending order
			{Key: "loginHashEmail", Value: 1}, // 1 for ascending order
		},
	}); err != nil {
		return fmt.Errorf("failed to create index on censusId and loginHashEmail for censusParticipants: %w", err)
	}

	// unique index over userID and processID
	if _, err := ms.cspTokensStatus.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "processid", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on userid and processid for cspTokensStatus: %w", err)
	}

	// member properties text index for filtering
	if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "email", Value: "text"},
			{Key: "memberNumber", Value: "text"},
			{Key: "nationalID", Value: "text"},
			{Key: "name", Value: "text"},
			{Key: "surname", Value: "text"},
			{Key: "birthDate", Value: "text"},
		},
	}); err != nil {
		return fmt.Errorf("failed to create text index on orgMembers: %w", err)
	}

	// Individual indexes for regex search optimization on orgMembers
	// These indexes improve performance for regex queries on individual fields
	orgMemberRegexIndexes := []struct {
		field string
		key   string
	}{
		{"email", "email"},
		{"memberNumber", "memberNumber"},
		{"nationalID", "nationalID"},
		{"name", "name"},
		{"surname", "surname"},
		{"birthDate", "birthDate"},
	}

	for _, idx := range orgMemberRegexIndexes {
		if _, err := ms.orgMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: idx.key, Value: 1}}, // 1 for ascending order
		}); err != nil {
			return fmt.Errorf("failed to create index on %s for orgMembers: %w", idx.field, err)
		}
	}

	// create an index for the jobId field on jobs (must be unique)
	if _, err := ms.jobs.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "jobId", Value: 1}}, // 1 for ascending order
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to create index on jobId for jobs: %w", err)
	}

	// create an index for the orgAddress field on jobs
	if _, err := ms.jobs.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "orgAddress", Value: 1}}, // 1 for ascending order
	}); err != nil {
		return fmt.Errorf("failed to create index on orgAddress for jobs: %w", err)
	}

	return nil
}

func downInitialIndexes(ctx context.Context, database *mongo.Database) error {
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
