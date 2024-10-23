package db

import "go.mongodb.org/mongo-driver/bson"

var collectionsValidators = map[string]bson.M{
	"users":         usersCollectionValidator,
	"organizations": organizationsCollectionValidator,
	"subscriptions": subscriptionCollectionValidator,
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

var organizationsCollectionValidator = bson.M{
	"$jsonSchema": bson.M{
		"bsonType": "object",
		"required": []string{"_id", "name"},
		"properties": bson.M{
			// list properties as define in db/types.go
			"id": bson.M{
				"bsonType":    "string",
				"description": "must be a string and is required",
			},
			"name": bson.M{
				"bsonType":    "string",
				"description": "must be a string and is required",
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
			// 	"organization": bson.M{
			// 		"bsonType":    "object",
			// 		"description": "the organization limits must be an object and is required",
			// 		// "required":    []string{"memberships", "subOrgs", "maxCensusSize"},
			// 		"properties": bson.M{
			// 			"memberships": bson.M{
			// 				"bsonType":    "int",
			// 				"description": "the max number of memberships allowed must be an integer and is required",
			// 				"minimum":     1,
			// 			},
			// 			"subOrgs": bson.M{
			// 				"bsonType":    "int",
			// 				"description": "the max number of sub organizations allowed must be an integer and is required",
			// 				"minimum":     1,
			// 			},
			// 			"maxCensusSize": bson.M{
			// 				"bsonType":    "int",
			// 				"description": "the max number of participants allowed in the each election must be an integer and is required",
			// 				"minimum":     1,
			// 			},
			// 		},
			// 	},
			// 	"votingTypes": bson.M{
			// 		"bsonType":    "object",
			// 		"description": "the voting types allowed must be an object and is required",
			// 		// "required":    []string{"approval", "ranked", "weighted"},
			// 		"properties": bson.M{
			// 			"approval": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "approval voting must be a boolean and is required",
			// 			},
			// 			"ranked": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "ranked voting must be a boolean and is required",
			// 			},
			// 			"weighted": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "weighted voting must be a boolean and is required",
			// 			},
			// 		},
			// 	},
			// 	"features": bson.M{
			// 		"bsonType":    "object",
			// 		"description": "the features enabled must be an object and is required",
			// 		// "required":    []string{"personalization", "emailReminder", "smsNotification"},
			// 		"properties": bson.M{
			// 			"personalization": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "personalization must be a boolean and is required",
			// 			},
			// 			"emailReminder": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "emailReminder must be a boolean and is required",
			// 			},
			// 			"smsNotification": bson.M{
			// 				"bsonType":    "bool",
			// 				"description": "smsNotification must be a boolean and is required",
			// 			},
			// 		},
			// 	},
		},
	},
}
