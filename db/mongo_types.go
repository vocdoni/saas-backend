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

type PlanCollection struct {
	Plans []Plan `json:"plans" bson:"plans"`
}

type OrganizationInvitesCollection struct {
	OrganizationInvites []OrganizationInvite `json:"organizationInvites" bson:"organizationInvites"`
}

type CensusCollection struct {
	Census []Census `json:"census" bson:"census"`
}

type CensusParticipantCollection struct {
	CensusParticipants []CensusParticipant `json:"censusParticipants" bson:"censusParticipants"`
}

type Collection struct {
	UserCollection
	UserVerifications
	OrganizationCollection
	OrganizationInvitesCollection
}
