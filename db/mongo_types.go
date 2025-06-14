package db

//revive:disable:max-public-structs

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
	Censuses []Census `json:"census" bson:"census"`
}

type OrgMembersCollection struct {
	OrgMembers []OrgMember `json:"orgMembers" bson:"orgMembers"`
}

type CensusMembershipsCollection struct {
	CensusMemberships []CensusMembership `json:"censusMemberships" bson:"censusMemberships"`
}

type PublishedCensusesCollection struct {
	PublishedCensuses []PublishedCensus `json:"publishedCensuses" bson:"publishedCensuses"`
}

type ProcessesCollection struct {
	Processes []Process `json:"processes" bson:"processes"`
}

type Collection struct {
	UserCollection
	UserVerifications
	OrganizationCollection
	OrganizationInvitesCollection
	CensusCollection
	OrgMembersCollection
	CensusMembershipsCollection
	PublishedCensusesCollection
	ProcessesCollection
}
