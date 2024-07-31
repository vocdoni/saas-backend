package mongo

import "go.mongodb.org/mongo-driver/bson/primitive"

type User struct {
	ID       primitive.ObjectID `json:"id" bson:"_id"`
	Email    string             `json:"email" bson:"email"`
	Password string             `json:"password" bson:"password"`
}

type UserCollection struct {
	Users []User `json:"users" bson:"users"`
}

type Collection struct {
	UserCollection
}
