package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	AddMigration(8, "enforce_unique_login_hashes", upEnforceUniqueLoginHashes, downEnforceUniqueLoginHashes)
}

func upEnforceUniqueLoginHashes(ctx context.Context, database *mongo.Database) error {
	return replaceIndex(ctx, database.Collection("censusParticipants"),
		[]string{
			"censusId_1_loginHash_1",
			"censusId_1_loginHashPhone_1",
			"censusId_1_loginHashEmail_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHash", Value: 1},
				},
				Options: options.Index().
					SetUnique(true).
					SetPartialFilterExpression(bson.M{
						"loginHash": bson.M{"$exists": true, "$type": "binData"},
					}),
			},
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHashPhone", Value: 1},
				},
				Options: options.Index().
					SetUnique(true).
					SetPartialFilterExpression(bson.M{
						"loginHashPhone": bson.M{"$exists": true, "$type": "binData"},
					}),
			},
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHashEmail", Value: 1},
				},
				Options: options.Index().
					SetUnique(true).
					SetPartialFilterExpression(bson.M{
						"loginHashEmail": bson.M{"$exists": true, "$type": "binData"},
					}),
			},
		})
}

func downEnforceUniqueLoginHashes(ctx context.Context, database *mongo.Database) error {
	return replaceIndex(ctx, database.Collection("censusParticipants"),
		[]string{
			"censusId_1_loginHash_1",
			"censusId_1_loginHashPhone_1",
			"censusId_1_loginHashEmail_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHash", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHashPhone", Value: 1},
				},
			},
			{
				Keys: bson.D{
					{Key: "censusId", Value: 1},
					{Key: "loginHashEmail", Value: 1},
				},
			},
		})
}
