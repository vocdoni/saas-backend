package db

//revive:disable:max-public-structs

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type User struct {
	ID            uint64             `json:"id" bson:"_id"`
	Email         string             `json:"email" bson:"email"`
	Password      string             `json:"password" bson:"password"`
	FirstName     string             `json:"firstName" bson:"firstName"`
	LastName      string             `json:"lastName" bson:"lastName"`
	Organizations []OrganizationUser `json:"organizations" bson:"organizations"`
	Verified      bool               `json:"verified" bson:"verified"`
}

type CodeType string

type UserVerification struct {
	ID         uint64    `json:"id" bson:"_id"`
	Code       string    `json:"code" bson:"code"`
	Type       CodeType  `json:"type" bson:"type"`
	Expiration time.Time `json:"expiration" bson:"expiration"`
}

// TODO this is the default role function while it should be
// used only when it is not necessary to consult the DB
func (u *User) HasRoleFor(address common.Address, role UserRole) bool {
	for _, org := range u.Organizations {
		if org.Address == address &&
			// Check if the role matches the organization role
			string(org.Role) == string(role) {
			return true
		}
	}
	return false
}

type UserRole string

type OrganizationType string

type OrganizationUser struct {
	Address common.Address `json:"address" bson:"_id"` // common.Address is serialized as bytes in the db
	Role    UserRole       `json:"role" bson:"role"`
}

type Organization struct {
	Address         common.Address           `json:"address" bson:"_id"` // common.Address is serialized as bytes in the db
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
	Parent          common.Address           `json:"parent" bson:"parent"`
	Meta            map[string]any           `json:"meta" bson:"meta"`
	Subscription    OrganizationSubscription `json:"subscription" bson:"subscription"`
	Counters        OrganizationCounters     `json:"counters" bson:"counters"`
}

type PlanLimits struct {
	Users        int `json:"users" bson:"users"`
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
	PhoneSupport    bool `json:"phoneSupport" bson:"phoneSupport"`
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
	Users      int `json:"users" bson:"users"`
	Processes  int `json:"processes" bson:"processes"`
}

type OrganizationInvite struct {
	ID                  primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	InvitationCode      string             `json:"invitationCode" bson:"invitationCode"`
	OrganizationAddress common.Address     `json:"organizationAddress" bson:"organizationAddress"`
	CurrentUserID       uint64             `json:"currentUserID" bson:"currentUserID"`
	NewUserEmail        string             `json:"newUserEmail" bson:"newUserEmail"`
	Role                UserRole           `json:"role" bson:"role"`
	Expiration          time.Time          `json:"expiration" bson:"expiration"`
}

// Object represents a user uploaded object Includes user defined ID and the data
// as a byte array.
type Object struct {
	ID          string    `json:"id" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Data        []byte    `json:"data" bson:"data" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
	UserID      string    `json:"userId" bson:"userId"`
	ContentType string    `json:"contentType" bson:"contentType"`
}

// CensusType defines the type of census.
type CensusType string

const (
	// CensusTypeMail is used when the organizer uploads a list of names, memberIDs and e‑mails.
	CensusTypeAuthOnly  CensusType = "auth"
	CensusTypeMail      CensusType = "mail"
	CensusTypeSMS       CensusType = "sms"
	CensusTypeSMSorMail CensusType = "sms_or_mail"
)

// Census represents the information of a set of census participants
type Census struct {
	ID          primitive.ObjectID   `json:"id" bson:"_id,omitempty"`
	OrgAddress  common.Address       `json:"orgAddress" bson:"orgAddress"`
	Type        CensusType           `json:"type" bson:"type"`
	Size        int64                `json:"size" bson:"size"`
	GroupID     primitive.ObjectID   `json:"groupId" bson:"groupId"`
	Published   PublishedCensus      `json:"published" bson:"published"`
	AuthFields  OrgMemberAuthFields  `json:"authFields" bson:"orgMemberAuthFields"`
	TwoFaFields OrgMemberTwoFaFields `json:"twoFaFields" bson:"orgMemberTwoFaFields"`

	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}

// An org member belongs to an organization and her details that will be
// used for verification and/or authentication
// A member is tied to an organization by the orgAddress
//
//nolint:lll
type OrgMember struct {
	// Also referred to as member UID
	ID primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	// OrgAddress can be used for future sharding
	OrgAddress     common.Address `json:"orgAddress" bson:"orgAddress"`
	Email          string         `json:"email" bson:"email"`
	Phone          string         `json:"phone" bson:"phone"`
	HashedPhone    []byte         `json:"hashedPhone" bson:"hashedPhone" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	MemberNumber   string         `json:"memberNumber" bson:"memberNumber"`
	NationalID     string         `json:"nationalID" bson:"nationalID"`
	Name           string         `json:"name" bson:"name"`
	Surname        string         `json:"surname" bson:"surname"`
	BirthDate      string         `json:"birthDate" bson:"birthDate"`
	ParsedBirtDate time.Time      `json:"parsedBirthDate" bson:"parsedBirthDate"`
	Password       string         `json:"password" bson:"password"`
	HashedPass     []byte         `json:"pass" bson:"pass" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	Other          map[string]any `json:"other" bson:"other"`
	CreatedAt      time.Time      `json:"createdAt" bson:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt" bson:"updatedAt"`
}

// OrgMemberAuthFields defines the fields that can be used for member authentication.
type OrgMemberAuthField string

const (
	OrgMemberAuthFieldsName         OrgMemberAuthField = "name"
	OrgMemberAuthFieldsSurname      OrgMemberAuthField = "surname"
	OrgMemberAuthFieldsMemberNumber OrgMemberAuthField = "memberNumber"
	OrgMemberAuthFieldsNationalID   OrgMemberAuthField = "nationalID"
	OrgMemberAuthFieldsBirthDate    OrgMemberAuthField = "birthDate"
)

// OrgMemberAuthFields is a list of fields that can be used for member authentication.
type OrgMemberAuthFields []OrgMemberAuthField

// OrgMemberTwoFaField defines the fields that can be used for two-factor authentication.
type OrgMemberTwoFaField string

const (
	OrgMemberTwoFaFieldEmail OrgMemberTwoFaField = "email"
	OrgMemberTwoFaFieldPhone OrgMemberTwoFaField = "phone"
)

// OrgMemberTwoFaFields is a list of fields that can be used for two-factor authentication.
type OrgMemberTwoFaFields []OrgMemberTwoFaField

// Contains checks if the field is present in the OrgMemberAuthFields.
func (f OrgMemberTwoFaFields) Contains(field OrgMemberTwoFaField) bool {
	for _, v := range f {
		if v == field {
			return true
		}
	}
	return false
}

func (f OrgMemberTwoFaFields) GetCensusType() CensusType {
	if f.Contains(OrgMemberTwoFaFieldPhone) && f.Contains(OrgMemberTwoFaFieldEmail) {
		return CensusTypeSMSorMail
	} else if f.Contains(OrgMemberTwoFaFieldPhone) {
		return CensusTypeSMS
	} else if f.Contains(OrgMemberTwoFaFieldEmail) {
		return CensusTypeMail
	}
	return CensusTypeAuthOnly
}

type OrgMemberAggregationResults struct {
	// MemberIDs is a list of member IDs that are result of the aggregation
	Members []primitive.ObjectID `json:"memberIds" bson:"memberIds"`
	// Duplicates is a list of member IDs that were found to be duplicates
	Duplicates []primitive.ObjectID `json:"duplicates" bson:"duplicates"`
	// MissingData is a list of member IDs that had columns found to be empty
	MissingData []primitive.ObjectID `json:"missingData" bson:"missingData"`
}

// An Organization members' group is a precursor of a census, and is simply a
// collection of members that are grouped together for a specific purpose
type OrganizationMemberGroup struct {
	ID          primitive.ObjectID `json:"id" bson:"_id,omitempty"`
	OrgAddress  common.Address     `json:"orgAddress" bson:"orgAddress"`
	Title       string             `json:"title" bson:"title"`
	Description string             `json:"description" bson:"description"`
	MemberIDs   []string           `json:"memberIds" bson:"memberIds"`
	CreatedAt   time.Time          `json:"createdAt" bson:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt" bson:"updatedAt"`
	CensusIDs   []string           `json:"censusIds" bson:"censusIds"`
}

// Relates an OrgMember to a Census
// The censusID is the hex format in string of the objectID
type CensusParticipant struct {
	ParticipantID string    `json:"participantID" bson:"participantID"`
	CensusID      string    `json:"censusId" bson:"censusId"`
	CreatedAt     time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt" bson:"updatedAt"`
}

// Represents a published census as a census is represented in the vochain
// The publishedCensus is tied to a Census
type PublishedCensus struct {
	URI       string            `json:"uri" bson:"uri"`
	Root      internal.HexBytes `json:"root" bson:"root"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdAt"`
}

// Process represents a voting process in the vochain
// A process is tied to an organization by the orgAddress
// and to a publishedCensus
//
//nolint:lll
type Process struct {
	ID         internal.HexBytes `json:"id" bson:"_id"  swaggertype:"string" format:"hex" example:"deadbeef"`
	OrgAddress common.Address    `json:"orgAdress" bson:"orgAddress"`
	Census     Census            `json:"census" bson:"census"`
	Metadata   []byte            `json:"metadata,omitempty"  bson:"metadata"  swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// ProcessesBundle represents a group of voting processes that share a common census.
// This allows users to participate in multiple voting processes using the same authentication mechanism.
//
//nolint:lll
type ProcessesBundle struct {
	ID         primitive.ObjectID  `json:"id" bson:"_id,omitempty"`                                                               // Unique identifier for the bundle
	Census     Census              `json:"census" bson:"census"`                                                                  // The census associated with this bundle
	OrgAddress common.Address      `json:"orgAddress" bson:"orgAddress"`                                                          // The organization that owns this bundle
	Processes  []internal.HexBytes `json:"processes" bson:"processes" swaggertype:"array,string" format:"hex" example:"deadbeef"` // Array of process IDs included in this bundle
}
