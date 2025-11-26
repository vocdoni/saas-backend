package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(4, "rename_nationalid", upRenameNationalID, downRenameNationalID)
}

func upRenameNationalID(ctx context.Context, database *mongo.Database) error {
	return renameFieldAndReindex(ctx, database.Collection("orgMembers"), "nationalID", "nationalId",
		[]string{
			"email_text_memberNumber_text_nationalID_text_name_text_surname_text_birthDate_text",
			"nationalID_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "email", Value: "text"},
					{Key: "memberNumber", Value: "text"},
					{Key: "nationalId", Value: "text"},
					{Key: "name", Value: "text"},
					{Key: "surname", Value: "text"},
					{Key: "birthDate", Value: "text"},
				},
			},
			{
				Keys: bson.D{{Key: "nationalId", Value: 1}},
			},
		})
}

func downRenameNationalID(ctx context.Context, database *mongo.Database) error {
	return renameFieldAndReindex(ctx, database.Collection("orgMembers"), "nationalId", "nationalID",
		[]string{
			"email_text_memberNumber_text_nationalId_text_name_text_surname_text_birthDate_text",
			"nationalId_1",
		},
		[]mongo.IndexModel{
			{
				Keys: bson.D{
					{Key: "email", Value: "text"},
					{Key: "memberNumber", Value: "text"},
					{Key: "nationalID", Value: "text"},
					{Key: "name", Value: "text"},
					{Key: "surname", Value: "text"},
					{Key: "birthDate", Value: "text"},
				},
			},
			{
				Keys: bson.D{{Key: "nationalID", Value: 1}},
			},
		})
}
