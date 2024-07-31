package db

type UserCollection struct {
	Users []User `json:"users" bson:"users"`
}

type Collection struct {
	UserCollection
}
