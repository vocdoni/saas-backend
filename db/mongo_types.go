package db

type UserCollection struct {
	Users []User `json:"users" bson:"users"`
}

type OrganizationCollection struct {
	Organizations []Organization `json:"organizations" bson:"organizations"`
}

type Collection struct {
	UserCollection
	OrganizationCollection
}
