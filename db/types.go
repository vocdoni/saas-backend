package db

import (
	"time"
)

type User struct {
	ID            uint64               `json:"id" bson:"_id"`
	Email         string               `json:"email" bson:"email"`
	Password      string               `json:"password" bson:"password"`
	FirstName     string               `json:"firstName" bson:"firstName"`
	LastName      string               `json:"lastName" bson:"lastName"`
	Organizations []OrganizationMember `json:"organizations" bson:"organizations"`
	Verified      bool                 `json:"verified" bson:"verified"`
}

type CodeType string

type UserVerification struct {
	ID         uint64    `json:"id" bson:"_id"`
	Code       string    `json:"code" bson:"code"`
	Type       CodeType  `json:"type" bson:"type"`
	Expiration time.Time `json:"expiration" bson:"expiration"`
}

func (u *User) HasRoleFor(address string, role UserRole) bool {
	for _, org := range u.Organizations {
		if org.Address == address && string(org.Role) == string(role) {
			return true
		}
	}
	return false
}

type UserRole string

type OrganizationType string

type OrganizationMember struct {
	Address string   `json:"address" bson:"_id"`
	Role    UserRole `json:"role" bson:"role"`
}

type Organization struct {
	Address         string                   `json:"address" bson:"_id"`
	Website         string                   `json:"website" bson:"website"`
	Type            OrganizationType         `json:"type" bson:"type"`
	Creator         string                   `json:"creator" bson:"creator"`
	CreatedAt       time.Time                `json:"createdAt" bson:"createdAt"`
	Nonce           string                   `json:"nonce" bson:"nonce"`
	Size            string                   `json:"size" bson:"size"`
	Color           string                   `json:"color" bson:"color"`
	Subdomain       string                   `json:"subdomain" bson:"subdomain"`
	Country         string                   `json:"country" bson:"country"`
	Timezone        string                   `json:"timezone" bson:"timezone"`
	Active          bool                     `json:"active" bson:"active"`
	TokensPurchased uint64                   `json:"tokensPurchased" bson:"tokensPurchased"`
	TokensRemaining uint64                   `json:"tokensRemaining" bson:"tokensRemaining"`
	Parent          string                   `json:"parent" bson:"parent"`
	Subscription    OrganizationSubscription `json:"subscription" bson:"subscription"`
	Counters        OrganizationCounters     `json:"counters" bson:"counters"`
}
type SubscriptionLimits struct {
	Memberships int `json:"memberships" bson:"memberships"`
	SubOrgs     int `json:"subOrgs" bson:"subOrgs"`
	CensusSize  int `json:"censusSize" bson:"censusSize"`
}

type VotingTypes struct {
	Approval bool `json:"approval" bson:"approval"`
	Ranked   bool `json:"ranked" bson:"ranked"`
	Weighted bool `json:"weighted" bson:"weighted"`
}

type Features struct {
	Personalization bool `json:"personalization" bson:"personalization"`
	EmailReminder   bool `json:"emailReminder" bson:"emailReminder"`
	SmsNotification bool `json:"smsNotification" bson:"smsNotification"`
}

type Subscription struct {
	ID           uint64             `json:"id" bson:"_id"`
	Name         string             `json:"name" bson:"name"`
	StripeID     string             `json:"stripeID" bson:"stripeID"`
	Default      bool               `json:"default" bson:"default"`
	Organization SubscriptionLimits `json:"organization" bson:"organization"`
	VotingTypes  VotingTypes        `json:"votingTypes" bson:"votingTypes"`
	Features     Features           `json:"features" bson:"features"`
}

type OrganizationSubscription struct {
	SubscriptionID uint64    `bson:"subscriptionID"`
	StartDate      time.Time `bson:"startDate"`
	EndDate        time.Time `bson:"endDate"`
	RenewalDate    time.Time `bson:"renewalDate"`
	Active         bool      `bson:"active"`
	MaxCensusSize  int       `bson:"maxCensusSize"`
}

type OrganizationCounters struct {
	SentSMS    int `bson:"sentSMS"`
	SentEmails int `bson:"sentEmails"`
	SubOrgs    int `bson:"subOrgs"`
	Members    int `bson:"members"`
}

type OrganizationInvite struct {
	InvitationCode      string    `json:"invitationCode" bson:"invitationCode"`
	OrganizationAddress string    `json:"organizationAddress" bson:"organizationAddress"`
	CurrentUserID       uint64    `json:"currentUserID" bson:"currentUserID"`
	NewUserEmail        string    `json:"newUserEmail" bson:"newUserEmail"`
	Role                UserRole  `json:"role" bson:"role"`
	Expiration          time.Time `json:"expiration" bson:"expiration"`
}
