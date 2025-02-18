package db

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
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
		if org.Address == address &&
			// Check if the role is "any: or if the role matches the organization role
			(string(role) == string(AnyRole) || string(org.Role) == string(role)) {
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
	Communications  bool                     `json:"communications" bson:"communications"`
	TokensPurchased uint64                   `json:"tokensPurchased" bson:"tokensPurchased"`
	TokensRemaining uint64                   `json:"tokensRemaining" bson:"tokensRemaining"`
	Parent          string                   `json:"parent" bson:"parent"`
	Subscription    OrganizationSubscription `json:"subscription" bson:"subscription"`
	Counters        OrganizationCounters     `json:"counters" bson:"counters"`
}

type PlanLimits struct {
	Members      int `json:"members" bson:"members"`
	SubOrgs      int `json:"subOrgs" bson:"subOrgs"`
	MaxProcesses int `json:"maxProcesses" bson:"maxProcesses"`
	MaxCensus    int `json:"maxCensus" bson:"maxCensus"`
	// Max process duration in days
	MaxDuration int  `json:"maxDuration" bson:"maxDuration"`
	CustomURL   bool `json:"customURL" bson:"customURL"`
	Drafts      int  `json:"drafts" bson:"drafts"`
}

type VotingTypes struct {
	Single     bool `json:"single" bson:"single"`
	Multiple   bool `json:"multiple" bson:"multiple"`
	Approval   bool `json:"approval" bson:"approval"`
	Cumulative bool `json:"cumulative" bson:"cumulative"`
	Ranked     bool `json:"ranked" bson:"ranked"`
	Weighted   bool `json:"weighted" bson:"weighted"`
}

type Features struct {
	Anonymous       bool `json:"anonymous" bson:"anonymous"`
	Overwrite       bool `json:"overwrite" bson:"overwrite"`
	LiveResults     bool `json:"liveResults" bson:"liveResults"`
	Personalization bool `json:"personalization" bson:"personalization"`
	EmailReminder   bool `json:"emailReminder" bson:"emailReminder"`
	SmsNotification bool `json:"smsNotification" bson:"smsNotification"`
	WhiteLabel      bool `json:"whiteLabel" bson:"whiteLabel"`
	LiveStreaming   bool `json:"liveStreaming" bson:"liveStreaming"`
}

type Plan struct {
	ID              uint64      `json:"id" bson:"_id"`
	Name            string      `json:"name" bson:"name"`
	StripeID        string      `json:"stripeID" bson:"stripeID"`
	StripePriceID   string      `json:"stripePriceID" bson:"stripePriceID"`
	StartingPrice   int64       `json:"startingPrice" bson:"startingPrice"`
	Default         bool        `json:"default" bson:"default"`
	Organization    PlanLimits  `json:"organization" bson:"organization"`
	VotingTypes     VotingTypes `json:"votingTypes" bson:"votingTypes"`
	Features        Features    `json:"features" bson:"features"`
	CensusSizeTiers []PlanTier  `json:"censusSizeTiers" bson:"censusSizeTiers"`
}

type PlanTier struct {
	Amount int64 `json:"amount" bson:"amount"`
	UpTo   int64 `json:"upTo" bson:"upTo"`
}

type OrganizationSubscription struct {
	PlanID          uint64    `json:"planID" bson:"planID"`
	StartDate       time.Time `json:"startDate" bson:"startDate"`
	RenewalDate     time.Time `json:"renewalDate" bson:"renewalDate"`
	LastPaymentDate time.Time `json:"lastPaymentDate" bson:"lastPaymentDate"`
	Active          bool      `json:"active" bson:"active"`
	MaxCensusSize   int       `json:"maxCensusSize" bson:"maxCensusSize"`
	Email           string    `json:"email" bson:"email"`
}

type OrganizationCounters struct {
	SentSMS    int `json:"sentSMS" bson:"sentSMS"`
	SentEmails int `json:"sentEmails" bson:"sentEmails"`
	SubOrgs    int `json:"subOrgs" bson:"subOrgs"`
	Members    int `json:"members" bson:"members"`
	Processes  int `json:"processes" bson:"processes"`
}

type OrganizationInvite struct {
	InvitationCode      string    `json:"invitationCode" bson:"invitationCode"`
	OrganizationAddress string    `json:"organizationAddress" bson:"organizationAddress"`
	CurrentUserID       uint64    `json:"currentUserID" bson:"currentUserID"`
	NewUserEmail        string    `json:"newUserEmail" bson:"newUserEmail"`
	Role                UserRole  `json:"role" bson:"role"`
	Expiration          time.Time `json:"expiration" bson:"expiration"`
}

// Object represents a user uploaded object Includes user defined ID and the data
// as a byte array.
type Object struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Data        []byte    `json:"data" bson:"data"`
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
	UserID      string    `json:"userId" bson:"userId"`
	ContentType string    `json:"contentType" bson:"contentType"`
}

// CensusType defines the type of census.
type CensusType string

const (
	// CensusTypeMail is used when the organizer uploads a list of names, participantIDs and e‑mails.
	CensusTypePass      CensusType = "pass"
	CensusTypeMail      CensusType = "mail"
	CensusTypeSMS       CensusType = "sms"
	CensusTypeSMSorMail CensusType = "sms_or_mail"
)

// Census represents the information of a set of census participants
type Census struct {
	ID         primitive.ObjectID `json:"id" bson:"_id"`
	OrgAddress string             `json:"orgAddress" bson:"orgAddress"`
	Type       CensusType         `json:"type" bson:"type"`
	Processes  [][]byte           `json:"processes" bson:"processes"`
	CreatedAt  time.Time          `json:"createdAt" bson:"createdAt"`
	UpdatedAt  time.Time          `json:"updatedAt" bson:"updatedAt"`
}

type CensusParticipant struct {
	ID primitive.ObjectID `json:"id" bson:"_id"`
	// TODO talk with PM that this can increase infinitely
	CensusID      primitive.ObjectID     `json:"censusId" bson:"censusId"`
	Email         string                 `json:"email" bson:"email"`
	HashedEmail   []byte                 `json:"hashedEmail" bson:"hashedEmail"`
	Phone         string                 `json:"phone" bson:"phone"`
	HashedPhone   []byte                 `json:"hashedPhone" bson:"hashedPhone"`
	ParticipantID string                 `json:"participantId" bson:"participantId"`
	Name          string                 `json:"name" bson:"name"`
	Password      string                 `json:"password" bson:"password"`
	HashedPass    []byte                 `json:"pass" bson:"pass"`
	Other         map[string]interface{} `json:"other" bson:"other"`
	CreatedAt     time.Time              `json:"createdAt" bson:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt" bson:"updatedAt"`
}

// The following is only necessary in case the census is published by the saas
type PublishedCensus struct {
	ID        []byte    `json:"id" bson:"_id"`
	Census    Census    `json:"census" bson:"census"`
	URI       string    `json:"uri" bson:"uri"`
	Root      []byte    `json:"root" bson:"root"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

type Process struct {
	ID              []byte          `json:"id" bson:"_id"`
	OrgID           string          `json:"orgID" bson:"orgID"`
	PublishedCensus PublishedCensus `json:"publishedCensus" bson:"publishedCensus"`
	Metadata        []byte          `json:"metadata" bson:"metadata"`
}

// Utilizar non-salted key
