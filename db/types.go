package db

//revive:disable:max-public-structs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// OAuthProvider represents OAuth authentication credentials for a specific provider
type OAuthProvider struct {
	ExternalID        string    `json:"externalID" bson:"externalID"`               // User's Ethereum address from OAuth service
	SignatureHash     string    `json:"signatureHash" bson:"signatureHash"`         // Hashed UserOAuthSignature
	LinkedAt          time.Time `json:"linkedAt" bson:"linkedAt"`                   // When this provider was first linked
	LastAuthenticated time.Time `json:"lastAuthenticated" bson:"lastAuthenticated"` // Last successful authentication
}

type User struct {
	ID            uint64                   `json:"id" bson:"_id"`
	Email         string                   `json:"email" bson:"email"`
	Password      string                   `json:"password" bson:"password"` // Empty string for OAuth-only users
	FirstName     string                   `json:"firstName" bson:"firstName"`
	LastName      string                   `json:"lastName" bson:"lastName"`
	OAuth         map[string]OAuthProvider `json:"oauth,omitempty" bson:"oauth,omitempty"` // OAuth providers by name
	Organizations []OrganizationUser       `json:"organizations" bson:"organizations"`
	Verified      bool                     `json:"verified" bson:"verified"`
}

type CodeType string

type UserVerification struct {
	ID         uint64    `json:"id" bson:"_id"`
	SealedCode []byte    `json:"sealedCode" bson:"sealedCode"`
	Type       CodeType  `json:"type" bson:"type"`
	CreatedAt  time.Time `json:"createdAt" bson:"createdAt"`
	Expiration time.Time `json:"expiration" bson:"expiration"`
	Attempts   int       `json:"attempts" bson:"attempts"`
}

// TODO this is the default role function while it should be
// used only when it is not necessary to consult the DB
func (u *User) HasRoleFor(address common.Address, role UserRole) bool {
	currentRole, ok := u.RoleFor(address)
	return ok && currentRole == role
}

// RoleFor returns the role of the user in the organization with the given
// address
func (u *User) RoleFor(address common.Address) (UserRole, bool) {
	for _, org := range u.Organizations {
		if org.Address == address {
			return org.Role, true
		}
	}
	return "", false
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
	ManagedBy       common.Address           `json:"managedBy,omitempty" bson:"managedBy,omitempty"`
	// IntegratorLimits, when set, is a per-organization override that both enables
	// integrator status (manual/admin path) and caps its managed resources. When unset,
	// integrator status and limits derive from the active subscription plan instead.
	IntegratorLimits *IntegratorLimits `json:"integratorLimits,omitempty" bson:"integratorLimits,omitempty"`
}

// IntegratorLimits caps how many organizations an integrator may manage. The
// aggregate process and census-size caps across those managed orgs are taken from
// the integrator's plan top-level limits (Plan.Organization.MaxProcesses /
// MaxCensus), not duplicated here.
type IntegratorLimits struct {
	MaxManagedOrgs int `json:"maxManagedOrgs" bson:"maxManagedOrgs"`
}

type PlanLimits struct {
	Users        int `json:"teamMembers" bson:"users"`
	SubOrgs      int `json:"subOrgs" bson:"subOrgs"`
	MaxProcesses int `json:"maxProcesses" bson:"maxProcesses"`
	MaxCensus    int `json:"maxCensus" bson:"maxCensus"`
	// Max votes that may be relayed; 0 means unlimited. For managed orgs this is the
	// integrator's shared pool, summed across all its managed orgs.
	MaxVotes int `json:"maxVotes" bson:"maxVotes"`
	// Max process duration in days
	MaxDuration int  `json:"maxDaysDuration" bson:"maxDuration"`
	CustomURL   bool `json:"customURL" bson:"customURL"`
	MaxDrafts   int  `json:"drafts" bson:"drafts"`
	CustomPlan  bool `json:"customPlan" bson:"customPlan"`
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
	TwoFaSms        int  `json:"2FAsms" bson:"twoFaSms"`
	TwoFaEmail      int  `json:"2FAemail" bson:"twoFaEmail"`
	WhiteLabel      bool `json:"whiteLabel" bson:"whiteLabel"`
	LiveStreaming   bool `json:"liveStreaming" bson:"liveStreaming"`
	PhoneSupport    bool `json:"phoneSupport" bson:"phoneSupport"`
}

// Plan is keyed by its Stripe product ID. Plans are not authored locally: they are synced
// from Stripe, which is the single source of truth for plan definitions (see
// stripe.Service.GetPlansFromStripe).
type Plan struct {
	ID                   string `json:"id" bson:"_id"`
	Name                 string `json:"name" bson:"name"`
	StripeMonthlyPriceID string `json:"stripeMonthlyPriceID" bson:"stripeMonthlyPriceID"`
	MonthlyPrice         int64  `json:"monthlyPrice" bson:"monthlyPrice"`
	StripeYearlyPriceID  string `json:"stripeYearlyPriceID" bson:"stripeYearlyPriceID"`
	YearlyPrice          int64  `json:"yearlyPrice" bson:"yearlyPrice"`
	Default              bool   `json:"default" bson:"default"`
	// Public reports whether the plan is listed on the public /plans catalog. Private plans
	// (custom per-customer contracts and the internal free integrator tier) are hidden from
	// the listing but remain visible to their own subscriber via the subscription payload.
	Public           bool             `json:"public" bson:"public"`
	FreeTrialDays    int              `json:"freeTrialDays" bson:"freeTrialDays"`
	Organization     PlanLimits       `json:"organization" bson:"organization"`
	VotingTypes      VotingTypes      `json:"votingTypes" bson:"votingTypes"`
	Features         Features         `json:"features" bson:"features"`
	IntegratorLimits IntegratorLimits `json:"integratorLimits" bson:"integratorLimits"`
}

type BillingPeriod string

const (
	// BillingPeriodMonthly indicates that the plan is billed monthly
	BillingPeriodMonthly BillingPeriod = "month"
	// BillingPeriodAnnual indicates that the plan is billed annually
	BillingPeriodAnnual BillingPeriod = "year"
)

type OrganizationSubscription struct {
	PlanID               string        `json:"planID" bson:"planID"`
	StripeSubscriptionID string        `json:"stripeSubscriptionID" bson:"stripeSubscriptionID"`
	BillingPeriod        BillingPeriod `json:"billingPeriod" bson:"billingPeriod"`
	StartDate            time.Time     `json:"startDate" bson:"startDate"`
	RenewalDate          time.Time     `json:"renewalDate" bson:"renewalDate"`
	LastPaymentDate      time.Time     `json:"lastPaymentDate" bson:"lastPaymentDate"`
	Active               bool          `json:"active" bson:"active"`
	Email                string        `json:"email" bson:"email"`
}

type OrganizationCounters struct {
	SentSMS          int `json:"sentSMS" bson:"sentSMS"`
	SentEmails       int `json:"sentEmails" bson:"sentEmails"`
	SentVotes        int `json:"sentVotes" bson:"sentVotes"`
	SubOrgs          int `json:"subOrgs" bson:"subOrgs"`
	Users            int `json:"users" bson:"users"`
	Processes        int `json:"processes" bson:"processes"`
	ManagedOrgs      int `json:"managedOrgs" bson:"managedOrgs"`
	ManagedProcesses int `json:"managedProcesses" bson:"managedProcesses"`
}

type OrganizationInvite struct {
	ID                  primitive.ObjectID `json:"id" bson:"_id"`
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
	ID          primitive.ObjectID   `json:"id" bson:"_id"`
	OrgAddress  common.Address       `json:"orgAddress" bson:"orgAddress"`
	Type        CensusType           `json:"type" bson:"type"`
	Weighted    bool                 `json:"weighted" bson:"weighted"`
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
	ID primitive.ObjectID `json:"id" bson:"_id"`
	// OrgAddress can be used for future sharding
	OrgAddress      common.Address `json:"orgAddress" bson:"orgAddress"`
	Email           string         `json:"email" bson:"email"`
	Phone           HashedPhone    `json:"phone" bson:"phone"`
	PlaintextPhone  string         `json:"-" bson:"-"`
	MemberNumber    string         `json:"memberNumber" bson:"memberNumber"`
	NationalID      string         `json:"nationalId" bson:"nationalId"`
	Name            string         `json:"name" bson:"name"`
	Surname         string         `json:"surname" bson:"surname"`
	BirthDate       string         `json:"birthDate" bson:"birthDate"`
	ParsedBirthDate time.Time      `json:"parsedBirthDate" bson:"parsedBirthDate"`
	Password        string         `json:"password" bson:"password"`
	HashedPass      []byte         `json:"pass" bson:"pass" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	Weight          uint64         `json:"weight" bson:"weight"`
	Other           map[string]any `json:"other" bson:"other"`
	CreatedAt       time.Time      `json:"createdAt" bson:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt" bson:"updatedAt"`
}

// OrgMemberAuthFields defines the fields that can be used for member authentication.
type OrgMemberAuthField string

// UnmarshalJSON validates JSON inputs for OrgMemberAuthField
func (f *OrgMemberAuthField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch OrgMemberAuthField(s) {
	case OrgMemberAuthFieldsName,
		OrgMemberAuthFieldsSurname,
		OrgMemberAuthFieldsMemberNumber,
		OrgMemberAuthFieldsNationalID,
		OrgMemberAuthFieldsBirthDate:
		*f = OrgMemberAuthField(s)
		return nil
	default:
		return fmt.Errorf("invalid OrgMemberAuthField: %s", s)
	}
}

const (
	OrgMemberAuthFieldsName         OrgMemberAuthField = "name"
	OrgMemberAuthFieldsSurname      OrgMemberAuthField = "surname"
	OrgMemberAuthFieldsMemberNumber OrgMemberAuthField = "memberNumber"
	OrgMemberAuthFieldsNationalID   OrgMemberAuthField = "nationalId"
	OrgMemberAuthFieldsBirthDate    OrgMemberAuthField = "birthDate"
)

// OrgMemberAuthFields is a list of fields that can be used for member authentication.
type OrgMemberAuthFields []OrgMemberAuthField

// OrgMemberTwoFaField defines the fields that can be used for two-factor authentication.
type OrgMemberTwoFaField string

// UnmarshalJSON validates JSON inputs for OrgMemberTwoFaField
func (f *OrgMemberTwoFaField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch OrgMemberTwoFaField(s) {
	case OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone:
		*f = OrgMemberTwoFaField(s)
		return nil
	default:
		return fmt.Errorf("invalid OrgMemberTwoFaField: %s", s)
	}
}

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

// HashAuthTwoFaFields helper function receives as input the data of a member and
// the auth and twoFa field and produces a sha256 hash of the concatenation of the
// data that are included in the fields. The data are ordered by the field names
// in order to make the hash reproducible.
func HashAuthTwoFaFields(memberData OrgMember, authFields OrgMemberAuthFields, twoFaFields OrgMemberTwoFaFields) []byte {
	data := make([]string, 0, len(twoFaFields)+len(authFields))
	for _, field := range authFields {
		switch field {
		case OrgMemberAuthFieldsName:
			data = append(data, memberData.Name)
		case OrgMemberAuthFieldsSurname:
			data = append(data, memberData.Surname)
		case OrgMemberAuthFieldsMemberNumber:
			data = append(data, memberData.MemberNumber)
		case OrgMemberAuthFieldsNationalID:
			data = append(data, memberData.NationalID)
		case OrgMemberAuthFieldsBirthDate:
			data = append(data, memberData.BirthDate)
		default:
			// Ignore unknown fields
			continue
		}
	}
	for _, field := range twoFaFields {
		switch field {
		case OrgMemberTwoFaFieldEmail:
			data = append(data, memberData.Email)
		case OrgMemberTwoFaFieldPhone:
			if !memberData.Phone.IsEmpty() {
				data = append(data, string(memberData.Phone))
			}
		default:
			// Ignore unknown fields
			continue
		}
	}
	return internal.HashSortedFields(data)
}

type OrgMemberAggregationResults struct {
	// MemberIDs is a list of member IDs that are result of the aggregation
	Members []primitive.ObjectID `json:"memberIds" bson:"memberIds"`
	// Duplicates is a list of member IDs that were found to be duplicates
	Duplicates []primitive.ObjectID `json:"duplicates" bson:"duplicates"`
	// MissingData is a list of member IDs that had columns found to be empty
	MissingData []primitive.ObjectID `json:"missingData" bson:"missingData"`
	// NotFound is a list of requested member IDs that matched no member of the organization
	// (unknown, or belonging to another org). Only populated for the explicit-memberIds path.
	NotFound []primitive.ObjectID `json:"notFound" bson:"notFound"`
}

const (
	// AutoGroupTitle is the title of the auto-generated group that always contains every member.
	AutoGroupTitle = "All members"
	// AutoGroupDescription is the description shown to users for the auto-generated group.
	AutoGroupDescription = "This group is automatically generated and always contains every member of your member base."
)

// An Organization members' group is a precursor of a census, and is simply a
// collection of members that are grouped together for a specific purpose
type OrganizationMemberGroup struct {
	ID          primitive.ObjectID `json:"id" bson:"_id"`
	OrgAddress  common.Address     `json:"orgAddress" bson:"orgAddress"`
	Title       string             `json:"title" bson:"title"`
	Description string             `json:"description" bson:"description"`
	// MemberIDs is intentionally empty for auto groups (IsAutoGroup == true).
	// Their membership is derived dynamically from the full orgMembers collection.
	MemberIDs []string  `json:"memberIds" bson:"memberIds"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
	CensusIDs []string  `json:"censusIds" bson:"censusIds"`
	// IsAutoGroup marks this group as the auto-generated "All members" group.
	// Auto groups cannot be deleted and their membership cannot be manually modified.
	IsAutoGroup bool `json:"isAutoGroup" bson:"isAutoGroup"`
}

// Relates an OrgMember to a Census
// The censusID is the hex format in string of the objectID
//
//nolint:lll
type CensusParticipant struct {
	ParticipantID  string    `json:"participantID" bson:"participantID"`
	CensusID       string    `json:"censusId" bson:"censusId"`
	LoginHash      []byte    `json:"loginHash" bson:"loginHash" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	LoginHashPhone []byte    `json:"loginHashPhone" bson:"loginHashPhone" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	LoginHashEmail []byte    `json:"loginHashEmail" bson:"loginHashEmail" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
	CreatedAt      time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt" bson:"updatedAt"`
}

// Represents a published census as a census is represented in the vochain
// The publishedCensus is tied to a Census
type PublishedCensus struct {
	URI       string            `json:"uri" bson:"uri"`
	Root      internal.HexBytes `json:"root" bson:"root"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdAt"`
}

// MultiLangString holds language-keyed strings, e.g. {"default":"Hello","es":"Hola"}.
type MultiLangString map[string]string

// Choice is a single selectable option within a Question.
type Choice struct {
	Title MultiLangString `json:"title" bson:"title"`
	Value uint32          `json:"value" bson:"value"`
}

// Question is a single question of an election.
type Question struct {
	Title       MultiLangString `json:"title" bson:"title"`
	Description MultiLangString `json:"description,omitempty" bson:"description,omitempty"`
	Choices     []Choice        `json:"choices" bson:"choices"`
}

// VoteType describes how votes are counted and validated.
type VoteType struct {
	MaxCount          uint32 `json:"maxCount" bson:"maxCount"`
	MaxValue          uint32 `json:"maxValue" bson:"maxValue"`
	MaxVoteOverwrites uint32 `json:"maxVoteOverwrites" bson:"maxVoteOverwrites"`
	CostFromWeight    bool   `json:"costFromWeight" bson:"costFromWeight"`
	CostExponent      uint32 `json:"costExponent" bson:"costExponent"`
	UniqueChoices     bool   `json:"uniqueChoices" bson:"uniqueChoices"`
	// MaxTotalCost bounds the sum of value^costExponent across a ballot's fields
	// (0 = no limit). Used by multichoice/approval to cap the number of selections.
	MaxTotalCost uint32 `json:"maxTotalCost,omitempty" bson:"maxTotalCost,omitempty"`
}

// ElectionType describes the election envelope and mode flags.
type ElectionType struct {
	Autostart         bool `json:"autostart" bson:"autostart"`
	Interruptible     bool `json:"interruptible" bson:"interruptible"`
	DynamicCensus     bool `json:"dynamicCensus" bson:"dynamicCensus"`
	SecretUntilTheEnd bool `json:"secretUntilTheEnd" bson:"secretUntilTheEnd"`
	Anonymous         bool `json:"anonymous" bson:"anonymous"`
}

// EncryptionKey is a single election encryption public key for an encrypted
// (secretUntilTheEnd) election, identified by its keykeeper Index. Key holds the
// hex-encoded public key voters use to encrypt their ballots. Only public keys are
// ever represented here; the private keys revealed when the election ends are not.
type EncryptionKey struct {
	Index int               `json:"index" bson:"index"`
	Key   internal.HexBytes `json:"key" bson:"key" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// ElectionTypeMetadata is the metadata "type" block describing how results are displayed.
type ElectionTypeMetadata struct {
	Name       string `json:"name" bson:"name"`
	Properties any    `json:"properties,omitempty" bson:"properties,omitempty"`
}

// ElectionParams holds the high-level inputs for an election. At publish time these are
// mapped into the on-chain models.Process and an ElectionMetadata JSON document.
type ElectionParams struct {
	Title         MultiLangString       `json:"title" bson:"title"`
	Description   MultiLangString       `json:"description,omitempty" bson:"description,omitempty"`
	Header        string                `json:"header,omitempty" bson:"header,omitempty"`
	StreamURI     string                `json:"streamUri,omitempty" bson:"streamUri,omitempty"`
	StartDate     time.Time             `json:"startDate,omitempty" bson:"startDate,omitempty"`
	EndDate       time.Time             `json:"endDate,omitempty" bson:"endDate,omitempty"`
	Questions     []Question            `json:"questions" bson:"questions"`
	VoteType      VoteType              `json:"voteType" bson:"voteType"`
	ElectionType  ElectionType          `json:"electionType" bson:"electionType"`
	TypeMetadata  *ElectionTypeMetadata `json:"type,omitempty" bson:"type,omitempty"`
	MaxCensusSize uint64                `json:"maxCensusSize,omitempty" bson:"maxCensusSize,omitempty"`
}

// Process represents a voting process in the vochain
// A process is tied to an organization by the orgAddress
// and to a publishedCensus
//
//nolint:lll
type Process struct {
	ID         primitive.ObjectID `json:"id" bson:"_id"`
	Address    internal.HexBytes  `json:"address" bson:"address"  swaggertype:"string" format:"hex" example:"deadbeef"`
	OrgAddress common.Address     `json:"orgAdress" bson:"orgAddress"`
	Census     Census             `json:"census" bson:"census"`
	Metadata   map[string]any     `json:"metadata"  bson:"metadata"`
	// MetadataURL is the generic reference to this process's canonical ElectionMetadata
	// document. http(s) references are fetched — locally when they point at this service's
	// object storage (including a relative "/storage/{name}" reference), otherwise via an
	// external request; ipfs references are resolved via the Vochain and then cached
	// locally. Bootstrapped from the on-chain pointer on first read when unset. Unset for
	// unpublished drafts, and omitted from JSON in that case (omitempty).
	MetadataURL    string          `json:"metadataURL,omitempty" bson:"metadataURL,omitempty"`
	ElectionParams *ElectionParams `json:"electionParams,omitempty" bson:"electionParams,omitempty"`
	Status         string          `json:"status,omitempty" bson:"status,omitempty"`
	PublishedAt    time.Time       `json:"publishedAt,omitempty" bson:"publishedAt,omitempty"`
	// EncryptionKeys holds the on-chain encryption public keys of an encrypted
	// (secretUntilTheEnd) election. They are resolved lazily on read and cached here once
	// the keykeepers have published them (immutable thereafter). Unset/omitted for
	// non-encrypted elections and for encrypted ones whose keys are not yet published.
	EncryptionKeys []EncryptionKey `json:"encryptionKeys,omitempty" bson:"encryptionKeys,omitempty"`
}

// ProcessesBundle represents a group of voting processes that share a common census.
// This allows users to participate in multiple voting processes using the same authentication mechanism.
//
//nolint:lll
type ProcessesBundle struct {
	ID         primitive.ObjectID  `json:"id" bson:"_id"`                                                                         // Unique identifier for the bundle
	Census     Census              `json:"census" bson:"census"`                                                                  // The census associated with this bundle
	OrgAddress common.Address      `json:"orgAddress" bson:"orgAddress"`                                                          // The organization that owns this bundle
	Processes  []internal.HexBytes `json:"processes" bson:"processes" swaggertype:"array,string" format:"hex" example:"deadbeef"` // Array of process addresses included in this bundle
}

// QuestionTypeSetup carries the friendly ballot-type parameters for a
// VotingProcessQuestion. It is translated into on-chain vote options at publish
// time (see account.VoteTypeFromQuestion). MinChoices is a validation hint only
// (the current protocol has no on-chain minimum-count field).
type QuestionTypeSetup struct {
	MinChoices    uint32 `json:"minChoices" bson:"minChoices"`
	MaxChoices    uint32 `json:"maxChoices" bson:"maxChoices"`
	UniqueChoices bool   `json:"uniqueChoices" bson:"uniqueChoices"`
}

// BallotProtocol is an optional raw override of the on-chain ballot parameters. When
// set on a VotingProcessQuestion it takes priority over Type/TypeSetup and is mapped
// directly onto the election envelope and vote options, enabling ballot shapes
// (approval, ranked, quadratic) before named types exist for them.
type BallotProtocol struct {
	MaxCount          uint32 `json:"maxCount" bson:"maxCount"`
	MaxValue          uint32 `json:"maxValue" bson:"maxValue"`
	MaxVoteOverwrites uint32 `json:"maxVoteOverwrites" bson:"maxVoteOverwrites"`
	CostExponent      uint32 `json:"costExponent" bson:"costExponent"`
	MaxTotalCost      uint32 `json:"maxTotalCost" bson:"maxTotalCost"`
	UniqueValues      bool   `json:"uniqueValues" bson:"uniqueValues"`
	CostFromWeight    bool   `json:"costFromWeight" bson:"costFromWeight"`
}

// VotingProcess is the container document of the multi-question /processes API. It
// holds the shared parameters and a census, and references its questions (each an
// independent on-chain election) by id in the processesQuestions collection. It is a
// draft while Published is false. It is unrelated to the single-election Process type.
type VotingProcess struct {
	ID          primitive.ObjectID   `json:"id" bson:"_id"`
	OrgAddress  common.Address       `json:"orgAddress" bson:"orgAddress"`
	Published   bool                 `json:"published" bson:"published"`
	Title       MultiLangString      `json:"title" bson:"title"`
	Description MultiLangString      `json:"description,omitempty" bson:"description,omitempty"`
	Header      string               `json:"header,omitempty" bson:"header,omitempty"`
	StreamURI   string               `json:"streamUri,omitempty" bson:"streamUri,omitempty"`
	StartDate   time.Time            `json:"startDate,omitempty" bson:"startDate,omitempty"`
	EndDate     time.Time            `json:"endDate,omitempty" bson:"endDate,omitempty"`
	CensusID    primitive.ObjectID   `json:"-" bson:"censusId"`    // internal ref to a db.Census
	QuestionIDs []primitive.ObjectID `json:"-" bson:"questionIds"` // ordered question references
	CreatedAt   time.Time            `json:"createdAt" bson:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt" bson:"updatedAt"`
}

// VotingProcessQuestion is one question of a VotingProcess. Each question maps to
// exactly one on-chain election, identified after publish by UpstreamID. OrgAddress is
// denormalized from the parent process so the vote relay and the status syncer can
// resolve the owner without a join. Status is uppercase (matching the vochain), set to "READY"
// at publish and reconciled against the chain by the status syncer; it is empty for a draft.
//
//nolint:lll
type VotingProcessQuestion struct {
	ID                primitive.ObjectID `json:"id" bson:"_id"`
	ProcessID         primitive.ObjectID `json:"parentProcessId" bson:"processId"`
	OrgAddress        common.Address     `json:"-" bson:"orgAddress"`
	Order             int                `json:"-" bson:"order"`
	Title             MultiLangString    `json:"title" bson:"title"`
	Description       MultiLangString    `json:"description,omitempty" bson:"description,omitempty"`
	Choices           []Choice           `json:"choices" bson:"choices"`
	Type              string             `json:"type" bson:"type"`
	TypeSetup         QuestionTypeSetup  `json:"typeSetup" bson:"typeSetup"`
	BallotProtocol    *BallotProtocol    `json:"ballotProtocol,omitempty" bson:"ballotProtocol,omitempty"`
	SecretUntilTheEnd bool               `json:"secretUntilTheEnd" bson:"secretUntilTheEnd"`
	EligibleMemberIDs []string           `json:"eligibleMemberIds,omitempty" bson:"eligibleMemberIds"`
	Metadata          map[string]any     `json:"metadata,omitempty" bson:"metadata,omitempty"`
	UpstreamID        internal.HexBytes  `json:"upstreamId,omitempty" bson:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	MetadataURL       string             `json:"-" bson:"metadataURL,omitempty"`
	Status            string             `json:"status,omitempty" bson:"status,omitempty"`
	SyncedAt          time.Time          `json:"-" bson:"syncedAt,omitempty"`
	// EncryptionKeys are the on-chain vote-encryption public keys of this question's election,
	// resolved on read and cached (only for secretUntilTheEnd questions). Because of omitempty the
	// JSON field is absent (not an empty array) until the keykeepers publish the keys, so clients
	// treat its absence as "not yet published" and poll. Voters seal encrypted vote packages with these.
	EncryptionKeys []EncryptionKey `json:"encryptionKeys,omitempty" bson:"encryptionKeys,omitempty"`
	// Results is this question's live on-chain tally, resolved on read for any published (on-chain)
	// question; FinalResults marks live vs final. The results object itself is present whenever the
	// question is published — it's the inner per-choice matrix (QuestionResults.Results) that is omitted
	// until a tally exists (empty while a secretUntilTheEnd election is still encrypted, or before any
	// vote). The whole object is absent (omitempty) only for a draft (no election yet). Not persisted
	// (bson:"-") — recomputed from the chain each read. Only the single reads resolve it (GET
	// /processes/{id} and the public question read); the list endpoint leaves it nil to avoid an N+1
	// chain fan-out, so its absence in a LIST response means "not resolved here", not "not published".
	Results *QuestionResults `json:"results,omitempty" bson:"-"`
}

// QuestionResults is one question's on-chain election tally, resolved on read from its own election.
// MaxVoters is that election's maxCensusSize — already restricted to the question's eligibility subset
// (see account.ComputeMaxCensusSize) — not the whole process census.
type QuestionResults struct {
	VoteCount    uint64 `json:"voteCount"`
	MaxVoters    uint64 `json:"maxVoters"`
	FinalResults bool   `json:"finalResults"`
	// Results is the raw on-chain tally matrix (stringified big integers), one row per ballot field:
	// a single-choice question has one row indexed by choice value (0..MaxValue, so sparse choice
	// values leave empty buckets); a multi-choice question has one row per choice, each [notSelected,
	// selected]. Absent until the tally is published.
	Results [][]string `json:"results,omitempty"`
}

// QuestionStatusRef is the minimal projection of a published question the status syncer and the
// managed-org delete guard need: its on-chain election id, owning org, and stored status.
type QuestionStatusRef struct {
	UpstreamID internal.HexBytes `bson:"upstreamId"`
	OrgAddress common.Address    `bson:"orgAddress"`
	Status     string            `bson:"status"`
}

// HashedPhone represents a hashed phone number for database storage
type HashedPhone []byte

// NewHashedPhone creates a HashedPhone from a phone and organization.
// If phone is the empty string, returns an empty Phone and no error.
func NewHashedPhone(phone string, organization *Organization) (HashedPhone, error) {
	if phone == "" {
		return HashedPhone{}, nil
	}

	// Normalize and hash in one operation
	normalized, err := internal.SanitizeAndVerifyPhoneNumber(phone, organization.Country)
	if err != nil {
		return nil, err
	}
	return HashedPhone(internal.HashOrgData(organization.Address, normalized)), nil
}

// Matches checks if a HashedPhone matches another phone
func (hp HashedPhone) Matches(other HashedPhone) bool {
	return bytes.Equal(hp, other)
}

// String returns masked hash for display
func (hp HashedPhone) String() string {
	if len(hp) == 0 {
		return ""
	}
	hexHash := fmt.Sprintf("%x", []byte(hp))
	if len(hexHash) < 6 {
		return hexHash
	}
	return hexHash[:6] + "***"
}

// Bytes returns the raw bytes
func (hp HashedPhone) Bytes() []byte {
	return hp
}

// IsEmpty returns true if hash is empty
func (hp HashedPhone) IsEmpty() bool {
	return len(hp) == 0
}

// MarshalJSON implements the json.Marshaler interface (returns display version)
func (hp HashedPhone) MarshalJSON() ([]byte, error) {
	return json.Marshal(hp.String())
}

// JobType represents the type of import job
type JobType string

const (
	// JobTypeOrgMembers represents organization member import jobs
	JobTypeOrgMembers JobType = "org_members"
	// JobTypeCensusParticipants represents census participant import jobs
	JobTypeCensusParticipants JobType = "census_participants"
	// JobTypePublishProcess represents a draft-process publish (NEW_PROCESS) tx job
	JobTypePublishProcess JobType = "publish_process"
	// JobTypeSetProcessStatus represents a SET_PROCESS_STATUS tx job
	JobTypeSetProcessStatus JobType = "set_process_status"
	// JobTypeSetProcessCensus represents a SET_PROCESS_CENSUS tx job (raise maxCensusSize)
	JobTypeSetProcessCensus JobType = "set_process_census"
	// JobTypeRelayVote represents a vote-relay tx job
	JobTypeRelayVote JobType = "relay_vote"
	// JobTypePublishVotingProcess represents a multi-question voting-process publish
	// (batch of NEW_PROCESS txs) tx job
	JobTypePublishVotingProcess JobType = "publish_voting_process"
)

// IsValid reports whether t is one of the known job types.
func (t JobType) IsValid() bool {
	switch t {
	case JobTypeOrgMembers, JobTypeCensusParticipants, JobTypePublishProcess, JobTypeSetProcessStatus,
		JobTypeSetProcessCensus, JobTypeRelayVote, JobTypePublishVotingProcess:
		return true
	default:
		return false
	}
}

// JobStatus is the lifecycle state of a transaction job (see CreateTxJob/SetJobStatus).
type JobStatus string

const (
	// JobStatusPending means the tx has been enqueued but not yet confirmed on chain
	JobStatusPending JobStatus = "pending"
	// JobStatusCompleted means the tx was submitted and confirmed on chain
	JobStatusCompleted JobStatus = "completed"
	// JobStatusFailed means the tx submission/confirmation failed
	JobStatusFailed JobStatus = "failed"
)

// JobResult carries the public on-chain outcome of a transaction job. Fields are
// populated depending on the job type (publish → Address+Status; status → Status;
// vote → VoteID) and are omitted when empty.
type JobResult struct {
	Address internal.HexBytes `json:"address,omitempty" bson:"address,omitempty" swaggertype:"string" example:"deadbeef"`
	VoteID  internal.HexBytes `json:"voteID,omitempty" bson:"voteID,omitempty" swaggertype:"string" example:"deadbeef"`
	Status  string            `json:"status,omitempty" bson:"status,omitempty"`
}

// Job represents a persistent import or transaction job with its results and errors.
// This allows clients to query job status and errors even after server restarts.
type Job struct {
	ID          primitive.ObjectID `json:"id" bson:"_id"`
	JobID       string             `json:"jobId" bson:"jobId"`           // The hex job ID
	Type        JobType            `json:"type" bson:"type"`             // Job type constant
	OrgAddress  common.Address     `json:"orgAddress" bson:"orgAddress"` // For authorization
	Total       int                `json:"total" bson:"total"`           // Total items processed
	Added       int                `json:"added" bson:"added"`           // Items successfully added
	Errors      []string           `json:"errors" bson:"errors"`         // All errors encountered
	CreatedAt   time.Time          `json:"createdAt" bson:"createdAt"`
	CompletedAt time.Time          `json:"completedAt" bson:"completedAt"`
	// Transaction-job fields (JobTypePublishProcess/SetProcessStatus/RelayVote)
	Status JobStatus  `json:"status,omitempty" bson:"status,omitempty"` // pending|completed|failed
	Result *JobResult `json:"result,omitempty" bson:"result,omitempty"` // on-chain outcome when completed
	Error  string     `json:"error,omitempty" bson:"error,omitempty"`   // failure reason when failed
}
