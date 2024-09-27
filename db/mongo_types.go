package db

type UserCollection struct {
	Users []User `json:"users" bson:"users"`
}

type UserVerifications struct {
	Verifications []UserVerification `json:"verifications" bson:"verifications"`
}

type OrganizationCollection struct {
	Organizations []Organization `json:"organizations" bson:"organizations"`
}

type SubscriptionCollection struct {
	Subscriptions []Subscription `json:"subscriptions" bson:"subscriptions"`
}

type Collection struct {
	UserCollection
	OrganizationCollection
}
