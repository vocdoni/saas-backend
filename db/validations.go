package db

import "go.mongodb.org/mongo-driver/bson"

var collectionsValidators = map[string]bson.M{
	"users": usersCollectionValidator,
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
