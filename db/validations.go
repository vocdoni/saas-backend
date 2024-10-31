package db

import "go.mongodb.org/mongo-driver/bson"

var collectionsValidators = map[string]bson.M{
	"users":               usersCollectionValidator,
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
				"pattern":     `^[\w.\+\.\-]+@([\w\-]+\.)+[\w]{2,}$`,
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
				"bsonType":    "string",
				"description": "must be a string and is required",
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
				"pattern":     `^[\w.\-]+@([\w\-]+\.)+[\w]{2,}$`,
			},
			"expiration": bson.M{
				"bsonType":    "date",
				"description": "must be a date and is required",
			},
		},
	},
}
