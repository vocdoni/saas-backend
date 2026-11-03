package db

//revive:disable:max-public-structs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
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
	// SessionVersion is the version every JWT session of the user is bound to. It is bumped
	// (atomically, by the credential-update methods) whenever all existing sessions must be
	// revoked: password change, password reset or email change. Tokens minted with an older
	// version are rejected by the authenticator. It is never written by SetUser, so a stale
	// snapshot cannot roll a revocation back.
	SessionVersion uint64 `json:"-" bson:"sessionVersion"`
}

type CodeType string

type UserVerification struct {
	UserID     uint64    `json:"userId" bson:"userId"`
	SealedCode []byte    `json:"sealedCode" bson:"sealedCode"`
	Type       CodeType  `json:"type" bson:"type"`
	CreatedAt  time.Time `json:"createdAt" bson:"createdAt"`
	Expiration time.Time `json:"expiration" bson:"expiration"`
	// PendingEmail is the address an email-update code (CodeTypeUpdateEmail) was sent to; the
	// user's email is switched to it only once the code is confirmed.
	PendingEmail string `json:"pendingEmail,omitempty" bson:"pendingEmail,omitempty"`
	// Attempts counts failed guesses of the code; it starts at zero and is bounded by
	// VerificationCodeCheckAndAddAttempt.
	Attempts int `json:"attempts" bson:"attempts"`
	// Sends counts deliveries of the code (the initial email plus resends); it starts at one
	// and is bounded by VerificationCodeTrySend, independently of the guess budget.
	Sends int `json:"sends" bson:"sends"`
	// LastSentAt is when the code was last delivered, used to enforce the send cooldown.
	LastSentAt time.Time `json:"lastSentAt" bson:"lastSentAt"`
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
	Address   common.Address   `json:"address" bson:"_id"` // common.Address is serialized as bytes in the db
	Website   string           `json:"website" bson:"website"`
	Type      OrganizationType `json:"type" bson:"type"`
	Creator   string           `json:"creator" bson:"creator"`
	CreatedAt time.Time        `json:"createdAt" bson:"createdAt"`
	Nonce     string           `json:"nonce" bson:"nonce"`
	// SignerSeed is the immutable per-organization component of the signer key derivation
	// (secret + seed + nonce). It is set once at creation to the creator's email (migration
	// 0028 backfills it from Creator) and never rewritten — unlike Creator, which
	// ReplaceCreatorEmail rewrites on an email change — so the derived on-chain key cannot
	// silently change. Never exposed over the API.
	SignerSeed string `json:"-" bson:"signerSeed,omitempty"`
	Size       string `json:"size" bson:"size"`
	Color      string `json:"color" bson:"color"`
	Subdomain  string `json:"subdomain" bson:"subdomain"`
	Country    string `json:"country" bson:"country"`
	Timezone   string `json:"timezone" bson:"timezone"`
	// DefaultLang is the language of the notifications sent on behalf of the organization
	// (see apicommon.NotificationLang). Every organization has one, defaulting to "en".
	// It can be changed but not cleared, dynamicUpdateDocument skipping zero-valued fields.
	DefaultLang    string `json:"defaultLang,omitempty" bson:"defaultLang,omitempty"`
	Communications bool   `json:"communications" bson:"communications"`
	// CustomURLPaidAt is when the once-per-organization custom URL add-on was paid, and
	// CustomURLClaimedBy/At the process allowed to charge it before then: the same mechanism as
	// branding below (see OrgAddOn), never written by SetOrganization.
	CustomURLPaidAt    time.Time     `json:"customUrlPaidAt,omitzero" bson:"customUrlPaidAt,omitempty"`
	CustomURLClaimedBy bson.ObjectID `json:"-" bson:"customUrlClaimedBy,omitempty"`
	CustomURLClaimedAt time.Time     `json:"-" bson:"customUrlClaimedAt,omitempty"`

	// BrandingPaidAt is when the once-per-organization branding add-on was paid; zero means unpaid.
	BrandingPaidAt time.Time `json:"brandingPaidAt,omitzero" bson:"brandingPaidAt,omitempty"`
	// BrandingClaimedBy is the one process allowed to charge branding before it is paid, so two
	// drafts are not both charged (ClaimOrganizationAddOn). omitempty: claim filters match
	// on absence. Never written by SetOrganization.
	BrandingClaimedBy bson.ObjectID            `json:"-" bson:"brandingClaimedBy,omitempty"`
	BrandingClaimedAt time.Time                `json:"-" bson:"brandingClaimedAt,omitempty"`
	Parent            common.Address           `json:"parent" bson:"parent"`
	Meta              map[string]any           `json:"meta" bson:"meta"`
	Subscription      OrganizationSubscription `json:"subscription" bson:"subscription"`
	Counters          OrganizationCounters     `json:"counters" bson:"counters"`
	ManagedBy         common.Address           `json:"managedBy,omitempty" bson:"managedBy,omitempty"`
	// Integrator enables the organization to create and pay for managed organizations
	// (ignored on a managed organization itself).
	Integrator bool `json:"integrator,omitempty" bson:"integrator,omitempty"`
	// ManagedOrgsCreated counts every managed organization this integrator ever created, deleted
	// ones included: each one is an on-chain account the service funded. Only ever incremented
	// (IncrementManagedOrgsCreated); SetOrganization never writes it.
	ManagedOrgsCreated int `json:"-" bson:"managedOrgsCreated,omitempty"`
	// AddOnsDelivered are the once-per-organization add-ons a published process of the
	// organization, or one with elections mined, received: what an organization teardown records
	// before it drops any draft (RecordOrganizationAddOnsDelivered), so a retry after the drafts
	// or published processes are gone still knows them. Only ever grows; never written by
	// SetOrganization.
	AddOnsDelivered []OrgAddOn `json:"-" bson:"addOnsDelivered,omitempty"`
	// AddOnsOwedCredit are the credit keys (TeardownAddOnCreditKey: one per withholding row) of
	// the withheld once-per-organization add-on prices an organization teardown owes back:
	// recorded with the removal of the stamp (OweOrganizationAddOnCredit), so a teardown cut
	// before its credit applies it on retry, and removed once credited
	// (SettleOrganizationAddOnCredit). Never written by SetOrganization.
	AddOnsOwedCredit []string `json:"-" bson:"addOnsOwedCredit,omitempty"`
}

// metaDefaultString extracts the "default" locale value from a meta entry, stored
// either as a plain string (legacy) or as a locale map. The bson.D case covers a
// plain bson.Unmarshal, which ignores the client's DefaultDocumentMap.
func metaDefaultString(v any) string {
	switch m := v.(type) {
	case string:
		return m
	case map[string]string:
		return m["default"]
	case map[string]any:
		if s, ok := m["default"].(string); ok {
			return s
		}
		return ""
	case bson.D:
		for _, e := range m {
			if e.Key == "default" {
				if s, ok := e.Value.(string); ok {
					return s
				}
			}
		}
		return ""
	default:
		return ""
	}
}

// DisplayName returns the organization display name stored in its metadata,
// or an empty string if the organization has no name set.
func (o *Organization) DisplayName() string {
	if o == nil || o.Meta == nil {
		return ""
	}
	return metaDefaultString(o.Meta["name"])
}

// SignerSeedValue returns the seed component used to derive the organization signer,
// falling back to the creator email for rows the backfill migration has not stamped yet
// (the value the backfill copies into the seed).
func (o *Organization) SignerSeedValue() string {
	if o.SignerSeed != "" {
		return o.SignerSeed
	}
	return o.Creator
}

// LogoURL returns the organization logo URL stored in its metadata,
// or an empty string if the organization has no logo set.
func (o *Organization) LogoURL() string {
	if o == nil || o.Meta == nil {
		return ""
	}
	return metaDefaultString(o.Meta["logo"])
}

type PlanLimits struct {
	Users        int `json:"teamMembers" bson:"users"`
	SubOrgs      int `json:"subOrgs" bson:"subOrgs"`
	MaxProcesses int `json:"maxProcesses" bson:"maxProcesses"`
	MaxCensus    int `json:"maxCensus" bson:"maxCensus"`
	// Max votes that may be relayed; 0 means unlimited.
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
	Public        bool        `json:"public" bson:"public"`
	FreeTrialDays int         `json:"freeTrialDays" bson:"freeTrialDays"`
	Organization  PlanLimits  `json:"organization" bson:"organization"`
	VotingTypes   VotingTypes `json:"votingTypes" bson:"votingTypes"`
	Features      Features    `json:"features" bson:"features"`
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
	SentSMS    int `json:"sentSMS" bson:"sentSMS"`
	SentEmails int `json:"sentEmails" bson:"sentEmails"`
	SentVotes  int `json:"sentVotes" bson:"sentVotes"`
	SubOrgs    int `json:"subOrgs" bson:"subOrgs"`
	Users      int `json:"users" bson:"users"`
	Processes  int `json:"processes" bson:"processes"`
}

type OrganizationInvite struct {
	ID                  bson.ObjectID  `json:"id" bson:"_id"`
	InvitationCode      string         `json:"invitationCode" bson:"invitationCode"`
	OrganizationAddress common.Address `json:"organizationAddress" bson:"organizationAddress"`
	CurrentUserID       uint64         `json:"currentUserID" bson:"currentUserID"`
	NewUserEmail        string         `json:"newUserEmail" bson:"newUserEmail"`
	Role                UserRole       `json:"role" bson:"role"`
	Expiration          time.Time      `json:"expiration" bson:"expiration"`
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
	ID         bson.ObjectID  `json:"id" bson:"_id"`
	OrgAddress common.Address `json:"orgAddress" bson:"orgAddress"`
	Type       CensusType     `json:"type" bson:"type"`
	Weighted   bool           `json:"weighted" bson:"weighted"`
	// Anonymous marks a blind-CSP census: published with census origin OFF_CHAIN_CA_V2 and a blind
	// public-key root, so the CSP blind-signs ballots and cannot link a signature to the voter.
	Anonymous   bool                 `json:"anonymous" bson:"anonymous"`
	Size        int64                `json:"size" bson:"size"`
	GroupID     bson.ObjectID        `json:"groupId" bson:"groupId"`
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
	ID bson.ObjectID `json:"id" bson:"_id"`
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

	// NationalIDOriginal is the nationalId as imported when its DNI/NIE check letter was derived
	// rather than supplied (NationalID then holds the derived canonical form), so the organizer can
	// review the derivation. Empty when the letter was supplied or the value is not a DNI/NIE. Only
	// prepareOrgMember sets it, and an explicit nationalId update clears it (OrgMemberUpdate.applyTo).
	NationalIDOriginal string `json:"nationalIdOriginal,omitempty" bson:"nationalIdOriginal,omitempty"`
}

// OrgMemberUpdate is a change to an organization member. A nil field leaves the stored value as
// it is; a non-nil one replaces it, the empty string clearing the field.
type OrgMemberUpdate struct {
	ID           bson.ObjectID
	MemberNumber *string
	Name         *string
	Surname      *string
	NationalID   *string
	BirthDate    *string
	Email        *string
	// Phone is in plaintext, it is hashed when the member is written
	Phone *string
	// Password is in plaintext, it is hashed when the member is written
	Password *string
	Weight   *uint64
	Other    map[string]any
}

// applyTo returns a copy of the stored member with the update applied.
func (u *OrgMemberUpdate) applyTo(stored OrgMember) *OrgMember {
	member := stored
	member.ID = u.ID
	set := func(field *string, value *string) {
		if value != nil {
			*field = *value
		}
	}
	set(&member.MemberNumber, u.MemberNumber)
	set(&member.Name, u.Name)
	set(&member.Surname, u.Surname)
	if u.NationalID != nil {
		member.NationalID = *u.NationalID
		// clear the marker only when the nationalId actually changes; re-sending the stored canonical
		// value (or its original letterless form) keeps the marker so the organizer can still review
		// it. prepareOrgMember re-sets the marker if the genuinely new value is letterless.
		if internal.NormalizeNationalID(*u.NationalID).Normalized != stored.NationalID {
			member.NationalIDOriginal = ""
		}
	}
	set(&member.Email, u.Email)
	if u.BirthDate != nil {
		member.BirthDate = *u.BirthDate
		member.ParsedBirthDate = time.Time{}
	}
	if u.Phone != nil {
		member.Phone = nil
		member.PlaintextPhone = *u.Phone
	}
	if u.Password != nil {
		member.HashedPass = nil
		member.Password = *u.Password
	}
	if u.Weight != nil {
		member.Weight = *u.Weight
	}
	if u.Other != nil {
		member.Other = u.Other
	}
	return &member
}

// Normalized returns a copy of the member with every field that can feed the CSP
// login hash reduced to its canonical form: surrounding whitespace trimmed, the
// email lowercased, the birthdate canonicalized to YYYY-MM-DD, and a DNI/NIE
// nationalId canonicalized by internal.NormalizeNationalID (separators dropped,
// uppercased, zero-padded, a missing check letter derived).
//
// This is the single definition of that canonical form, and both sides of the
// login-hash comparison go through it: members are stored normalized, and the
// CSP normalizes the login request the same way before recomputing the hash.
// Keeping the two in one place is the point — the hash is a byte-exact match
// (see internal.HashLoginFields), so any drift between how a field is stored
// and how it is compared silently locks the member out.
//
// It only normalizes. Invalid values are passed through in their trimmed form
// rather than rejected, leaving validation to the caller: at login an
// unparseable value should simply fail to match a participant, while at write
// time prepareOrgMember still validates and reports what it rejects.
func (m *OrgMember) Normalized() *OrgMember {
	normalized := *m
	normalized.Name = strings.TrimSpace(normalized.Name)
	normalized.Surname = strings.TrimSpace(normalized.Surname)
	normalized.MemberNumber = strings.TrimSpace(normalized.MemberNumber)
	normalized.NationalID = internal.NormalizeNationalID(normalized.NationalID).Normalized
	normalized.Email = internal.NormalizeEmail(normalized.Email)
	normalized.BirthDate = internal.NormalizeBirthDate(normalized.BirthDate)
	return &normalized
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
		OrgMemberAuthFieldsBirthDate,
		OrgMemberAuthFieldsPassword:
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
	// OrgMemberAuthFieldsPassword is the member password. It never takes part in the login hash:
	// the CSP finds the participant on the other fields, then checks the password against the
	// member's stored hash (see OrgMember.MissingLoginData and ValidateCensusFields).
	OrgMemberAuthFieldsPassword OrgMemberAuthField = "password"
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
// the auth and twoFa field and produces the sha256 login hash of the values of
// those fields, keyed by field name (see internal.HashLoginFields).
//
// Text values are lowercased so that login is case-insensitive. That folding is
// deliberately confined to this function and never written back to the member:
// members keep the casing they were imported with, because that is what gets
// displayed and exported. OrgMember.Normalized governs what is stored; this
// governs what is compared. The phone is the exception — it is already a hash
// (see HashedPhone), so it is raw bytes rather than text and folding it would
// corrupt the value.
//
// strings.ToLower matches what internal.NormalizeEmail already applies to
// emails. A full Unicode case-fold would be marginally more correct for a few
// scripts, but diverging from the existing convention would be worse: the two
// sides of the comparison only agree because they fold identically.
//
// The password auth field is not part of the hash (memberAuthFieldValue reports it unknown): it is
// checked after the participant lookup instead, so passwords never affect loginHash, uniqueness or
// duplicate detection.
//
// IMPORTANT: migrations.hashMemberFields mirrors this function byte for byte so
// that the repair tooling can recompute stored hashes without importing db. Any
// change here MUST be made there too — db.TestRepairMatchesCanonicalHash guards that.
func HashAuthTwoFaFields(memberData OrgMember, authFields OrgMemberAuthFields, twoFaFields OrgMemberTwoFaFields) []byte {
	fields := make(map[string]string, len(twoFaFields)+len(authFields))
	for _, field := range authFields {
		// unknown fields are ignored
		if value, known := memberAuthFieldValue(&memberData, field); known {
			fields[string(field)] = strings.ToLower(value)
		}
	}
	for _, field := range twoFaFields {
		switch field {
		case OrgMemberTwoFaFieldEmail:
			fields[string(field)] = strings.ToLower(memberData.Email)
		case OrgMemberTwoFaFieldPhone:
			if !memberData.Phone.IsEmpty() {
				// already hashed bytes, not text: never folded
				fields[string(field)] = string(memberData.Phone)
			}
		default:
			// Ignore unknown fields
			continue
		}
	}
	return internal.HashLoginFields(fields)
}

type OrgMemberAggregationResults struct {
	// MemberIDs is a list of member IDs that are result of the aggregation
	Members []bson.ObjectID `json:"memberIds" bson:"memberIds"`
	// Duplicates is a list of member IDs that were found to be duplicates
	Duplicates []bson.ObjectID `json:"duplicates" bson:"duplicates"`
	// MissingData is a list of member IDs that had columns found to be empty
	MissingData []bson.ObjectID `json:"missingData" bson:"missingData"`
	// NotFound is a list of requested member IDs that matched no member of the organization
	// (unknown, or belonging to another org). Only populated for the explicit-memberIds path.
	NotFound []bson.ObjectID `json:"notFound" bson:"notFound"`
	// NationalIDWarnings are the non-blocking nationalId problems of the members, reported only when
	// nationalId is an auth field. They never make the census invalid.
	NationalIDWarnings []NationalIDWarning `json:"nationalIdWarnings,omitempty" bson:"nationalIdWarnings,omitempty"`
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
	ID          bson.ObjectID  `json:"id" bson:"_id"`
	OrgAddress  common.Address `json:"orgAddress" bson:"orgAddress"`
	Title       string         `json:"title" bson:"title"`
	Description string         `json:"description" bson:"description"`
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
	// OpenValue marks the one choice of a question that accepts a free-text voter memo; the memos
	// endpoint returns memos only for votes that selected this choice. At most one per question.
	OpenValue bool `json:"openValue,omitempty" bson:"openValue,omitempty"`
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
	// InitialStatus is the on-chain status the election is published with. Empty (the default)
	// means READY; "PAUSED" publishes it in the PAUSED state, so voting only opens once an
	// admin sets it to READY via SET_PROCESS_STATUS. Only "" / "READY" / "PAUSED" are
	// accepted, matching the vochain whitelist for NewProcess. Publish-time only: this field
	// is not updated when the on-chain status changes later.
	InitialStatus string `json:"initialStatus,omitempty" bson:"initialStatus,omitempty"`
}

// Process represents a voting process in the vochain
// A process is tied to an organization by the orgAddress
// and to a publishedCensus
//
//nolint:lll
type Process struct {
	ID         bson.ObjectID     `json:"id" bson:"_id"`
	Address    internal.HexBytes `json:"address" bson:"address"  swaggertype:"string" format:"hex" example:"deadbeef"`
	OrgAddress common.Address    `json:"orgAdress" bson:"orgAddress"`
	Census     Census            `json:"census" bson:"census"`
	Metadata   map[string]any    `json:"metadata"  bson:"metadata"`
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
	ID         bson.ObjectID       `json:"id" bson:"_id"`                                                                         // Unique identifier for the bundle
	Census     Census              `json:"census" bson:"census"`                                                                  // The census associated with this bundle
	OrgAddress common.Address      `json:"orgAddress" bson:"orgAddress"`                                                          // The organization that owns this bundle
	Processes  []internal.HexBytes `json:"processes" bson:"processes" swaggertype:"array,string" format:"hex" example:"deadbeef"` // Array of process addresses included in this bundle
}

// QuestionTypeSetup carries the friendly ballot-type parameters for a VotingProcessQuestion, and
// is kept in step with its BallotProtocol: whichever half is supplied, the other is derived
// (account.ResolveBallotShape), so the two always describe the same ballot.
//
// MinChoices is a validation hint only — the protocol has no on-chain minimum-count field, so it
// is the one value that cannot be derived. It is kept as sent for multichoice (clamped to
// MaxChoices), where a voter may legitimately be asked for at least N; for singlechoice it is
// always 1, that ballot being the single field a voter fills, so a stated 0 would describe a
// submission the chain cannot express. UniqueChoices is not supported by any named type and
// requests setting it on a multichoice are rejected: with one 0/1 field per choice, a
// unique-values ballot admits no vote at all (issue #619), while a voter already cannot select
// the same choice twice. A ranked ballot — the one shape that does use uniqueValues — is its own
// named type and derives that flag from the type, not from this field.
//
// Budget and CostExponent parametrise a cumulative ballot (budget/quadratic): Budget is the total
// credit a voter may spread across the choices (the protocol's MaxTotalCost) and CostExponent is 1
// for a linear budget or 2 for quadratic. They are ignored by every other type.
type QuestionTypeSetup struct {
	MinChoices    uint32 `json:"minChoices" bson:"minChoices"`
	MaxChoices    uint32 `json:"maxChoices" bson:"maxChoices"`
	UniqueChoices bool   `json:"uniqueChoices" bson:"uniqueChoices"`
	Budget        uint32 `json:"budget" bson:"budget"`
	CostExponent  uint32 `json:"costExponent" bson:"costExponent"`
}

// BallotProtocol is the on-chain ballot parameters of a VotingProcessQuestion, mapped directly
// onto the election envelope and vote options. Every question written since the two halves were
// reconciled carries one, derived from Type/TypeSetup when the client did not supply it.
//
// A client may supply it instead of a named type, which is what enables ballot shapes beyond the
// named ones. Supplying it alongside a type makes it authoritative — it is what reaches the chain
// — so the type is re-derived from it, and emptied when it encodes no named shape.
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
	ID          bson.ObjectID   `json:"id" bson:"_id"`
	OrgAddress  common.Address  `json:"orgAddress" bson:"orgAddress"`
	Published   bool            `json:"published" bson:"published"`
	Title       MultiLangString `json:"title" bson:"title"`
	Description MultiLangString `json:"description,omitempty" bson:"description,omitempty"`
	Header      string          `json:"header,omitempty" bson:"header,omitempty"`
	StreamURI   string          `json:"streamUri,omitempty" bson:"streamUri,omitempty"`
	StartDate   time.Time       `json:"startDate,omitempty" bson:"startDate,omitempty"`
	EndDate     time.Time       `json:"endDate,omitempty" bson:"endDate,omitempty"`
	// InitialStatus is the on-chain status every question's election is published with.
	// Empty (default) means READY; "PAUSED" publishes every question's election in the
	// PAUSED state, so voting only opens after an admin sets each question to READY via
	// SET_PROCESS_STATUS. Only "" / "READY" / "PAUSED" are accepted. Publish-time only:
	// after publish, each question's status evolves independently and this field is not
	// updated to reflect them (it documents how the process was published).
	InitialStatus string          `json:"initialStatus,omitempty" bson:"initialStatus,omitempty"`
	CensusID      bson.ObjectID   `json:"-" bson:"censusId"`    // internal ref to a db.Census
	QuestionIDs   []bson.ObjectID `json:"-" bson:"questionIds"` // ordered question references
	// AddOns are the paid per-process options; 2FA add-ons derive from the census instead.
	AddOns ProcessAddOns `json:"addOns,omitzero" bson:"addOns,omitempty"`
	// Publishing is the transient claim a publish worker holds on this process (see
	// ClaimVotingProcessForPublish). It is a struct field rather than only a raw $set so that
	// SetVotingProcess's ReplaceOne stops wiping a live claim, and so handlers can refuse to
	// mutate a process while it is being published.
	//
	// omitempty is load-bearing: the duplicate-publish guard matches on the field being absent
	// and the stale sweep on it being old, so a zero date persisted as a value would make every
	// draft ever created look like a crashed publish.
	Publishing time.Time `json:"-" bson:"publishing,omitempty"`
	// PublishingOwner is the random token held by the publish worker that owns the Publishing
	// claim; renewal and the owner-conditional clear/publish writes match on it, so a worker
	// whose stale claim was reclaimed cannot overwrite the new claimant's state.
	PublishingOwner string `json:"-" bson:"publishingOwner,omitempty"`
	// HasMinedElections is set as soon as a publish persists the first on-chain election of one of
	// the process's questions, and never cleared. SetVotingProcessDraft refuses to replace such a
	// process: a draft edit would rewrite the questions and erase their election ids. Keeping the
	// flag on the process document makes that refusal part of the draft write itself, rather than
	// a check of the questions collection that a publish could invalidate before the write lands.
	HasMinedElections bool      `json:"-" bson:"hasMinedElections,omitempty"`
	CreatedAt         time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt" bson:"updatedAt"`
}

// PublishInProgress reports whether a publish worker currently holds this process. A marker older
// than PublishStaleAfter counts as released, matching what ClaimVotingProcessForPublish reclaims,
// so a worker that crashed cannot block edits forever.
func (vp *VotingProcess) PublishInProgress() bool {
	return !vp.Publishing.IsZero() && time.Since(vp.Publishing) < PublishStaleAfter
}

// ProcessAddOns are the paid per-process options of the pay-per-process pricing model.
type ProcessAddOns struct {
	SignedCertificate bool `json:"signedCertificate,omitempty" bson:"signedCertificate,omitempty"`
	CustomURL         bool `json:"customUrl,omitempty" bson:"customUrl,omitempty"`
	Branding          bool `json:"branding,omitempty" bson:"branding,omitempty"`
}

// Has reports whether the draft selects the once-per-organization add-on.
func (p ProcessAddOns) Has(addOn OrgAddOn) bool {
	switch addOn {
	case OrgAddOnBranding:
		return p.Branding
	case OrgAddOnCustomURL:
		return p.CustomURL
	default:
		return false
	}
}

// OrgAddOn is an add-on paid once per organization: the first process that pays it stamps the
// organization, and the organization's later processes get it for free. Its value is the add-on's
// bson name in ProcessAddOns and ProcessPayment, and the prefix of its Organization fields.
type OrgAddOn string

// Once-per-organization add-ons.
const (
	OrgAddOnBranding  OrgAddOn = "branding"
	OrgAddOnCustomURL OrgAddOn = "customUrl"
)

// OrgAddOns lists every once-per-organization add-on.
var OrgAddOns = []OrgAddOn{OrgAddOnBranding, OrgAddOnCustomURL}

// IsValid reports whether a is a declared once-per-organization add-on.
func (a OrgAddOn) IsValid() bool { return a == OrgAddOnBranding || a == OrgAddOnCustomURL }

func (a OrgAddOn) paidAtField() string    { return string(a) + "PaidAt" }
func (a OrgAddOn) claimedByField() string { return string(a) + "ClaimedBy" }
func (a OrgAddOn) claimedAtField() string { return string(a) + "ClaimedAt" }

// AddOnPaidAt is when the organization paid addOn; zero means unpaid.
func (o *Organization) AddOnPaidAt(addOn OrgAddOn) time.Time {
	if addOn == OrgAddOnCustomURL {
		return o.CustomURLPaidAt
	}
	return o.BrandingPaidAt
}

// AddOnClaim is the process holding the organization's claim on addOn, and when it took it.
func (o *Organization) AddOnClaim(addOn OrgAddOn) (bson.ObjectID, time.Time) {
	if addOn == OrgAddOnCustomURL {
		return o.CustomURLClaimedBy, o.CustomURLClaimedAt
	}
	return o.BrandingClaimedBy, o.BrandingClaimedAt
}

// ProcessPaymentStatus is the lifecycle state of a voting process payment.
type ProcessPaymentStatus string

// Process payment statuses. A process with no payment document is free or not quoted yet. Pending
// is the checkout state machine's (a compare-and-set on the session); paid, failed and refunded
// are a display cache of the wallet ledger, refreshed by every money path under the payer's
// wallet lock, and may lag it after a crash until the next one.
const (
	// ProcessPaymentPending: a wallet top-up checkout is open for the process (CheckoutSessionID).
	// Consumed once that top-up was credited; it then only holds the add-on claims until the
	// publish it paid for debits the wallet.
	ProcessPaymentPending ProcessPaymentStatus = "pending"
	// ProcessPaymentFailed: a wallet payment a failed publish reverted; the process is payable again.
	ProcessPaymentFailed ProcessPaymentStatus = "failed"
	// ProcessPaymentPaid: debited from the payer's wallet. Only a refund leaves it.
	ProcessPaymentPaid ProcessPaymentStatus = "paid"
	// ProcessPaymentRefunded: credited back on draft delete, which then drops the row with the draft.
	ProcessPaymentRefunded ProcessPaymentStatus = "refunded"
)

// ProcessPayment is the payment state of a voting process, keyed by process id. It lives in its
// own collection so a draft save (a full replace) cannot wipe it. Every payment is a wallet
// debit: the card only ever tops a wallet up.
type ProcessPayment struct {
	ProcessID  bson.ObjectID        `json:"-" bson:"_id"`
	OrgAddress common.Address       `json:"-" bson:"orgAddress"`
	Status     ProcessPaymentStatus `json:"status" bson:"status"`
	// CheckoutSessionID is the wallet top-up checkout opened for this process, if any: the open
	// one while pending, the one that funded the debit once paid.
	CheckoutSessionID string `json:"-" bson:"checkoutSessionId,omitempty"`
	// Consumed marks a pending payment whose top-up the webhook credited: the session is spent,
	// and the publish it was opened for pays from the wallet like any other.
	Consumed bool `json:"-" bson:"consumed,omitempty"`
	// AmountCents is the net total in EUR cents, VAT excluded: while pending, the price the top-up
	// was opened for. Otherwise a display cache of what the process paid, refreshed by every money
	// path under the payer's wallet lock: what it paid is read off the wallet ledger
	// (ProcessLedgerState) plus BaseCents, never off this field.
	AmountCents pricing.Cents `json:"amountCents" bson:"amountCents"`
	// TopUpCents is what the pending top-up checkout charges (the shortfall, at least the
	// minimum charge).
	TopUpCents pricing.Cents `json:"-" bson:"topUpCents,omitempty"`
	// BaseCents is what a published process paid outside the wallet, written once when it is
	// published: 0 for a process published with no price left to pay, the census price for one
	// published before pay-per-process. Nil while it is a draft. What the process paid is its
	// ledger (ProcessLedgerState.PaidCents) plus this.
	BaseCents *pricing.Cents `json:"-" bson:"baseCents,omitempty"`
	Currency  string         `json:"currency" bson:"currency"`
	// RequestedBy is the checkout user's email; the top-up webhook publishes as this user.
	RequestedBy string `json:"-" bson:"requestedBy,omitempty"`
	// PublishJobID is the job of the latest publish of the process (by hand or by the top-up
	// webhook), so its payment status can point at it.
	PublishJobID string `json:"-" bson:"publishJobId,omitempty"`
	// TopUpRefusal is why the top-up webhook did not publish the process once its checkout was
	// credited; the credit stays in the wallet and the process is published by hand. Written over
	// by the next payment record (a publish, a new checkout), which drops it.
	TopUpRefusal *TopUpRefusal `json:"-" bson:"topUpRefusal,omitempty"`
	// Branding and CustomURL are whether a pending payment's checkout priced each
	// once-per-organization add-on in. Once charged, the ledger's payment row carries the add-ons
	// (WalletLedgerEntry.AddOns).
	Branding  bool      `json:"-" bson:"branding,omitempty"`
	CustomURL bool      `json:"-" bson:"customUrl,omitempty"`
	PaidAt    time.Time `json:"paidAt,omitzero" bson:"paidAt,omitempty"`
	CreatedAt time.Time `json:"-" bson:"createdAt"`
	UpdatedAt time.Time `json:"-" bson:"updatedAt"`
}

// TopUpRefusal is the refusal the top-up webhook met publishing a process: the API error code
// and message a manual publish would have answered.
type TopUpRefusal struct {
	Code    int    `json:"code" bson:"code"`
	Message string `json:"message" bson:"message"`
}

// Charged reports whether the pending payment's checkout priced the once-per-organization add-on in.
func (p *ProcessPayment) Charged(addOn OrgAddOn) bool {
	switch addOn {
	case OrgAddOnBranding:
		return p.Branding
	case OrgAddOnCustomURL:
		return p.CustomURL
	default:
		return false
	}
}

// Wallet is a payer's prepaid EUR balance (1 credit = 1 euro, VAT excluded). AppliedKeys is a
// bounded window of recent idempotency keys that makes each write atomic; walletLedger is the
// permanent record. BalanceCents is negative only after a Stripe refund of a top-up already spent.
type Wallet struct {
	OrgAddress   common.Address `json:"orgAddress" bson:"_id"`
	BalanceCents pricing.Cents  `json:"balanceCents" bson:"balanceCents"`
	AppliedKeys  []string       `json:"-" bson:"appliedKeys"`
	// Unposted holds the movements applied to the balance whose walletLedger row may not be
	// written yet: each movement is pushed here in the same write that moves the balance, then
	// copied to the ledger and pulled, by the movement itself or by whatever touches the wallet
	// next (drain). So a movement is never in the balance without its record.
	Unposted []WalletLedgerEntry `json:"-" bson:"unposted,omitempty"`
	// OrgSpend is what each managed organization may spend and has spent from this wallet, keyed
	// by the managed organization's lowercase hex address (see ManagedOrgSpend). It lives on the
	// wallet so the cap is checked in the same conditional write as the balance.
	OrgSpend  map[string]ManagedOrgSpend `json:"-" bson:"orgSpend,omitempty"`
	UpdatedAt time.Time                  `json:"updatedAt" bson:"updatedAt"`
}

// ManagedOrgSpend is one managed organization's spending from its integrator's wallet. CapCents
// is the most it may have debited at once, set by the integrator; nil is unlimited. SpentCents is
// what its processes hold debited now: debits add to it, and credits returning a process's money
// (a delete's refund, a failed publish's credit-back) take it off again, so withheld branding
// stays counted.
type ManagedOrgSpend struct {
	CapCents   *pricing.Cents `json:"capCents" bson:"capCents,omitempty"`
	SpentCents pricing.Cents  `json:"spentCents" bson:"spentCents"`
}

// Wallet ledger entry kinds.
const (
	// WalletEntryTopUp is a credit bought through a Stripe checkout (key: session id).
	WalletEntryTopUp = "topup"
	// WalletEntryPayment is a debit for a process: its publish or census headroom.
	WalletEntryPayment = "payment"
	// WalletEntryCreditBack returns a payment to the wallet: a deleted paid draft, a failed publish.
	WalletEntryCreditBack = "creditBack"
	// WalletEntryRefund is money a top-up's payer got back through Stripe (a refund made by hand
	// in the dashboard, or a dispute's withdrawn funds), negative; and, positive, its reversal when
	// Stripe undoes it (a refund that failed or was canceled, a dispute reinstated).
	WalletEntryRefund = "refund"
)

// WalletLedgerEntry is one append-only wallet balance change, signed as applied (negative for
// payments and refunds). IdempotencyKey is unique, so a replay is not recorded twice.
type WalletLedgerEntry struct {
	ID          bson.ObjectID  `json:"id" bson:"_id"`
	OrgAddress  common.Address `json:"orgAddress" bson:"orgAddress"`
	AmountCents pricing.Cents  `json:"amountCents" bson:"amountCents"`
	Kind        string         `json:"kind" bson:"kind"`
	// IdempotencyKey is unique, so a replay is not recorded twice.
	IdempotencyKey string        `json:"-" bson:"idempotencyKey"`
	ProcessID      bson.ObjectID `json:"processId,omitzero" bson:"processId,omitempty"`
	// ProcessOrgAddress is the organization owning ProcessID: a managed one for an integrator.
	ProcessOrgAddress common.Address `json:"processOrgAddress,omitzero" bson:"processOrgAddress,omitempty"`
	// PaymentIntentID is the Stripe payment intent of a top-up, which a refund names.
	PaymentIntentID string `json:"-" bson:"paymentIntentId,omitempty"`
	// GrossCents is what a top-up charged VAT included, to take a refund's VAT back out.
	GrossCents pricing.Cents `json:"-" bson:"grossCents,omitempty"`
	// WithheldCents and WithheldAddOns are what a creditBack kept back of the payment it returns:
	// the once-per-organization add-ons another process of the organization relies on, which stay
	// the organization's.
	WithheldCents  pricing.Cents `json:"withheldCents,omitempty" bson:"withheldCents,omitempty"`
	WithheldAddOns []OrgAddOn    `json:"withheldAddOns,omitempty" bson:"withheldAddOns,omitempty"`
	// PriceCents is the process price a payment brought the process to, and Attempt the attempt
	// its key was built with (the number of publishfail credit-backs before it).
	PriceCents pricing.Cents `json:"-" bson:"priceCents,omitempty"`
	Attempt    int           `json:"-" bson:"attempt,omitempty"`
	// AddOns are the once-per-organization add-ons a payment priced in and stamped.
	AddOns []OrgAddOn `json:"-" bson:"addOns,omitempty"`
	// ReleaseAddOns are the add-ons a creditBack gives back to the organization (charged and not
	// withheld): released by whatever touches the process next until a later payment follows.
	ReleaseAddOns []OrgAddOn `json:"-" bson:"releaseAddOns,omitempty"`
	CreatedAt     time.Time  `json:"createdAt" bson:"createdAt"`
}

// ProcessLedgerState is what a process's payer's wallet ledger says about it (ProcessLedgerState):
// the one source of what the process paid, its attempt, its add-ons and what it is still owed.
type ProcessLedgerState struct {
	Payer common.Address
	// PaidCents is the process's envelope: −Σ(amountCents + withheldCents) over its payment and
	// creditBack rows. A credit-back settles what it returned and what it withheld, since a
	// withheld add-on stays the organization's stamp, not the process's.
	PaidCents pricing.Cents
	// Attempt is the number of publishfail credit-backs: the attempt segment of the next debit key.
	Attempt int
	// AddOns are the once-per-organization add-ons the live attempt's payments priced in.
	AddOns []OrgAddOn
	// Refunded: a delete credited the process back ("refund:<process>"); it can only be deleted.
	Refunded bool
	// OwedRelease are the add-ons the last credit-back gives back, while no payment follows it,
	// and OwedSince that row's time: the release only touches claims taken before it.
	OwedRelease []OrgAddOn
	OwedSince   time.Time
	// CreditedBackAt is the time of the last credit-back, whatever followed it.
	CreditedBackAt time.Time
}

// Charged reports whether the live attempt's payments priced the add-on in.
func (s *ProcessLedgerState) Charged(addOn OrgAddOn) bool { return slices.Contains(s.AddOns, addOn) }

// VotingProcessQuestion is one question of a VotingProcess. Each question maps to
// exactly one on-chain election, identified after publish by UpstreamID. OrgAddress is
// denormalized from the parent process so the vote relay and the status syncer can
// resolve the owner without a join. Status is uppercase (matching the vochain), set to "READY"
// at publish and reconciled against the chain by the status syncer; it is empty for a draft.
//
//nolint:lll
type VotingProcessQuestion struct {
	ID                bson.ObjectID     `json:"id" bson:"_id,omitempty"`
	ProcessID         bson.ObjectID     `json:"parentProcessId" bson:"processId"`
	OrgAddress        common.Address    `json:"-" bson:"orgAddress"`
	Order             int               `json:"-" bson:"order"`
	Title             MultiLangString   `json:"title" bson:"title"`
	Description       MultiLangString   `json:"description,omitempty" bson:"description,omitempty"`
	Choices           []Choice          `json:"choices" bson:"choices"`
	Type              string            `json:"type" bson:"type"`
	TypeSetup         QuestionTypeSetup `json:"typeSetup" bson:"typeSetup"`
	BallotProtocol    *BallotProtocol   `json:"ballotProtocol,omitempty" bson:"ballotProtocol,omitempty"`
	SecretUntilTheEnd bool              `json:"secretUntilTheEnd" bson:"secretUntilTheEnd"`
	EligibleMemberIDs []string          `json:"eligibleMemberIds,omitempty" bson:"eligibleMemberIds"`
	Metadata          map[string]any    `json:"metadata,omitempty" bson:"metadata,omitempty"`
	UpstreamID        internal.HexBytes `json:"upstreamId,omitempty" bson:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	MetadataURL       string            `json:"-" bson:"metadataURL,omitempty"`
	Status            string            `json:"status,omitempty" bson:"status,omitempty"`
	SyncedAt          time.Time         `json:"-" bson:"syncedAt,omitempty"`
	// EndedAt is the actual on-chain moment this question's election stopped accepting votes,
	// set only when the vote was ended early (before its scheduled end, i.e. ManuallyEnded on
	// the chain). Absent while the vote is open or when it ran to its scheduled end. Recorded by
	// the status syncer, and filled on the first detail read for questions it no longer visits
	// (RESULTS), so older votes get it on GET /processes/{id} or GET /processes/{id}/questions/{qid}.
	EndedAt time.Time `json:"endedAt,omitzero" bson:"endedAt,omitempty"`
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
	// Memos are the free-text voter memos cast alongside the question's open-value choice, one entry
	// per such vote. Manager/Admin-only — absent for public callers — and only for a question that has
	// an openValue choice. Live: empty while a secretUntilTheEnd election is still encrypted (the memo
	// cannot be correlated to a choice until the vote package is decryptable at RESULTS).
	Memos []string `json:"memos,omitempty"`
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
	return hashPhoneWith(phone, organization, func(org common.Address, normalized string) ([]byte, error) {
		return internal.HashOrgData(org, normalized), nil
	})
}

// NewHashedPhoneContext is NewHashedPhone for a request handler: the hash gives up with
// internal.ErrHashingBusy when no hashing slot frees up in time (see internal.HashOrgDataContext).
func NewHashedPhoneContext(ctx context.Context, phone string, organization *Organization) (HashedPhone, error) {
	return hashPhoneWith(phone, organization, func(org common.Address, normalized string) ([]byte, error) {
		return internal.HashOrgDataContext(ctx, org, normalized)
	})
}

// hashPhoneWith normalizes phone for the organization's country and hashes it with hash.
func hashPhoneWith(phone string, organization *Organization,
	hash func(org common.Address, normalized string) ([]byte, error),
) (HashedPhone, error) {
	if phone == "" {
		return HashedPhone{}, nil
	}
	normalized, err := internal.SanitizeAndVerifyPhoneNumber(phone, organization.Country)
	if err != nil {
		return nil, err
	}
	hashed, err := hash(organization.Address, normalized)
	if err != nil {
		return nil, err
	}
	return HashedPhone(hashed), nil
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
	// JobTypeRelayVotes represents a batch vote-relay tx job: one job covering the
	// several envelopes of a single POST /votes call, one JobResult.Votes entry each
	JobTypeRelayVotes JobType = "relay_votes"
	// JobTypePublishVotingProcess represents a multi-question voting-process publish
	// (batch of NEW_PROCESS txs) tx job
	JobTypePublishVotingProcess JobType = "publish_voting_process"
	// JobTypeSetProcessEndDate represents the SET_PROCESS_DURATION tx job that moves the end date
	// of every open (READY or PAUSED) published question of a voting process
	JobTypeSetProcessEndDate JobType = "set_process_end_date"
)

// IsValid reports whether t is one of the known job types.
func (t JobType) IsValid() bool {
	switch t {
	case JobTypeOrgMembers, JobTypeCensusParticipants, JobTypePublishProcess, JobTypeSetProcessStatus,
		JobTypeSetProcessCensus, JobTypeRelayVote, JobTypeRelayVotes, JobTypePublishVotingProcess,
		JobTypeSetProcessEndDate:
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

// VoteJobResult is the outcome of one relayed vote envelope inside a batch job
// (JobTypeRelayVotes). ProcessID and Nullifier are known before submission and are
// therefore set when the job is created, so a caller can tell which envelope an entry
// belongs to while it is still pending or after it failed. VoteID is the nullifier as
// returned by the chain and only appears once the vote has been accepted and mined.
type VoteJobResult struct {
	ProcessID internal.HexBytes `json:"processId,omitempty" bson:"processId,omitempty" swaggertype:"string" example:"deadbeef"`
	Nullifier internal.HexBytes `json:"nullifier,omitempty" bson:"nullifier,omitempty" swaggertype:"string" example:"deadbeef"`
	VoteID    internal.HexBytes `json:"voteID,omitempty" bson:"voteID,omitempty" swaggertype:"string" example:"deadbeef"`
	Status    JobStatus         `json:"status,omitempty" bson:"status,omitempty"`
	Error     string            `json:"error,omitempty" bson:"error,omitempty"`
}

// QuestionJobResult is the outcome of one question inside a status-change job
// (JobTypeSetProcessStatus) or a census-update job (JobTypeSetProcessCensus). NoOp marks a
// question that needed no on-chain transaction: already in the requested status, or, for a
// census update, an election that already has the room or is no longer open on chain.
type QuestionJobResult struct {
	QuestionID string    `json:"questionId" bson:"questionId"`
	Status     JobStatus `json:"status,omitempty" bson:"status,omitempty"`
	Error      string    `json:"error,omitempty" bson:"error,omitempty"`
	NoOp       bool      `json:"noOp,omitempty" bson:"noOp,omitempty"`
}

// JobResult carries the public on-chain outcome of a transaction job. Fields are
// populated depending on the job type (publish → Address+Status; status → Status+Questions;
// vote → ProcessID+Nullifier+VoteID; batch vote → Votes) and are omitted when empty.
type JobResult struct {
	Address   internal.HexBytes `json:"address,omitempty" bson:"address,omitempty" swaggertype:"string" example:"deadbeef"`
	ProcessID internal.HexBytes `json:"processId,omitempty" bson:"processId,omitempty" swaggertype:"string" example:"deadbeef"`
	Nullifier internal.HexBytes `json:"nullifier,omitempty" bson:"nullifier,omitempty" swaggertype:"string" example:"deadbeef"`
	VoteID    internal.HexBytes `json:"voteID,omitempty" bson:"voteID,omitempty" swaggertype:"string" example:"deadbeef"`
	Status    string            `json:"status,omitempty" bson:"status,omitempty"`
	// Votes carries the per-envelope outcome of a batch vote relay, index-aligned with
	// the votes of the POST /votes request that created the job.
	Votes []VoteJobResult `json:"votes,omitempty" bson:"votes,omitempty"`
	// Questions carries the per-question outcome of a status-change or census-update job, in target order.
	Questions []QuestionJobResult `json:"questions,omitempty" bson:"questions,omitempty"`
}

// Job represents a persistent import or transaction job with its results and errors.
// This allows clients to query job status and errors even after server restarts.
type Job struct {
	ID         bson.ObjectID  `json:"id" bson:"_id"`
	JobID      string         `json:"jobId" bson:"jobId"`           // The hex job ID
	Type       JobType        `json:"type" bson:"type"`             // Job type constant
	OrgAddress common.Address `json:"orgAddress" bson:"orgAddress"` // For authorization
	Total      int            `json:"total" bson:"total"`           // Total items processed
	// Added counts the items successfully added for import jobs. For a batch vote relay
	// (JobTypeRelayVotes) it counts the envelopes that have *finished*, successfully or
	// not, so that the progress reported to the caller advances on every outcome.
	Added       int       `json:"added" bson:"added"`
	Errors      []string  `json:"errors" bson:"errors"` // All errors encountered
	CreatedAt   time.Time `json:"createdAt" bson:"createdAt"`
	CompletedAt time.Time `json:"completedAt" bson:"completedAt"`
	// Transaction-job fields (JobTypePublishProcess/SetProcessStatus/RelayVote/RelayVotes)
	Status JobStatus  `json:"status,omitempty" bson:"status,omitempty"` // pending|completed|failed
	Result *JobResult `json:"result,omitempty" bson:"result,omitempty"` // on-chain outcome when completed
	Error  string     `json:"error,omitempty" bson:"error,omitempty"`   // failure reason when failed
	// Instance identifies the service process whose in-memory tx queue holds a pending
	// transaction job, and HeartbeatAt is the last time that process vouched it is still
	// alive. Together they let any replica close the jobs of a process that died without
	// touching the jobs another live replica is still running (see FailInterruptedTxJobs).
	Instance    string    `json:"-" bson:"instance,omitempty"`
	HeartbeatAt time.Time `json:"-" bson:"heartbeatAt,omitempty"`

	// NationalIDWarnings are the nationalId warnings of a member import job (JobTypeOrgMembers).
	// They carry DNI values: GET /jobs/{jobId} hides them from anonymous callers, like Errors.
	NationalIDWarnings []NationalIDWarning `json:"nationalIdWarnings,omitempty" bson:"nationalIdWarnings,omitempty"`
}
