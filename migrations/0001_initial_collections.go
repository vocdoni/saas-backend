package migrations

import (
	"context"
	"fmt"
	"slices"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(1, "initial_collections", upInitialCollections, downInitialCollections)
}

var collectionsToCreate = []string{
	"users",
	"verifications",
	"organizations",
	"organizationInvites",
	"plans",
	"objects",
	"census",
	"orgMembers",
	"orgMemberGroups",
	"censusParticipants",
	"publishedCensuses",
	"processes",
	"processBundles",
	"cspTokens",
	"cspTokensStatus",
	"jobs",
	"migrations",
}

var collectionsValidators = map[string]bson.M{
	"users":               usersCollectionValidator,
	"subscriptions":       subscriptionCollectionValidator,
	"organizationInvites": organizationInvitesCollectionValidator,
}

var usersCollectionValidator = bson.M{
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

var organizationInvitesCollectionValidator = bson.M{
	"$jsonSchema": bson.M{
		"bsonType": "object",
		"required": []string{"invitationCode", "organizationAddress", "currentUserID", "newUserEmail", "role", "expiration"},
		"properties": bson.M{
			"invitationCode": bson.M{
				"bsonType":    "string",
				"description": "must be a string and is required",
				"minimum":     6,
				"pattern":     `^[\w]{6,}$`,
			},
			"organizationAddress": bson.M{
				"bsonType":    "binData",
				"description": "must be binary data and is required",
			},
			"currentUserID": bson.M{
				"bsonType":    "long",
				"description": "must be an integer and is required",
				"minimum":     1,
				"pattern":     `^[1-9]+$`,
			},
			"newUserEmail": bson.M{
				"bsonType":    "string",
				"description": "must be an email and is required",
				"pattern":     internal.EmailRegexTemplate,
			},
			"expiration": bson.M{
				"bsonType":    "date",
				"description": "must be a date and is required",
			},
		},
	},
}

var subscriptionCollectionValidator = bson.M{
	"$jsonSchema": bson.M{
		"bsonType": "object",
		"required": []string{"_id", "name", "stripeID"},
		"properties": bson.M{
			"id": bson.M{
				"bsonType":    "int",
				"description": "must be an integer and is required",
				"minimum":     1,
			},
			"name": bson.M{
				"bsonType":    "string",
				"description": "the name of the subscription plan must be a string and is required",
			},
			"stripeID": bson.M{
				"bsonType":    "string",
				"description": "the corresponding plan ID must be a string and is required",
			},
		},
	},
}

func upInitialCollections(ctx context.Context, database *mongo.Database) error {
	// get the current collections names to create only the missing ones
	currentCollections, err := listCollectionsInDB(ctx, database)
	if err != nil {
		return fmt.Errorf("failed to get current collections: %w", err)
	}
	for _, name := range collectionsToCreate {
		// if the collection doesn't exist, create it
		if !slices.Contains(currentCollections, name) {
			// if the collection has a validator create it with it
			opts := options.CreateCollection()
			if validator, ok := collectionsValidators[name]; ok {
				opts = opts.SetValidator(validator).SetValidationLevel("strict").SetValidationAction("error")
			}
			// create the collection
			if err := database.CreateCollection(ctx, name, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func downInitialCollections(context.Context, *mongo.Database) error {
	// Strictly speaking, this down func would Drop all created collections, but that's too risky/destructive.
	// So we do nothing here. (the up func is idempotent anyway)
	return nil
}

// listCollectionsInDB returns the names of the collections in the given database.
// It uses the ListCollections method of the MongoDB client to get the
// collections info and decode the names from the result.
func listCollectionsInDB(ctx context.Context, database *mongo.Database) ([]string, error) {
	collectionsCursor, err := database.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := collectionsCursor.Close(ctx); err != nil {
			log.Warnw("failed to close collections cursor", "error", err)
		}
	}()
	collections := []bson.D{}
	if err := collectionsCursor.All(ctx, &collections); err != nil {
		return nil, err
	}
	names := []string{}
	for _, col := range collections {
		for _, v := range col {
			if v.Key == "name" {
				names = append(names, v.Value.(string))
			}
		}
	}
	return names, nil
}
