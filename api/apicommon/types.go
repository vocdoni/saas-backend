package apicommon

import (
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
)

// OrganizationInfo represents an organization in the API.
// swagger:model OrganizationInfo
type OrganizationInfo struct {
	// The organization's blockchain address
	Address string `json:"address"`

	// The organization's website URL
	Website string `json:"website"`

	// Creation timestamp in RFC3339 format
	CreatedAt string `json:"createdAt"`

	// The type of organization
	Type string `json:"type"`

	// The size category of the organization
	Size string `json:"size"`

	// The organization's brand color in hex format
	Color string `json:"color"`

	// The organization's subdomain
	Subdomain string `json:"subdomain"`

	// The country where the organization is based
	Country string `json:"country"`

	// The organization's timezone
	Timezone string `json:"timezone"`

	// Whether the organization is active
	Active bool `json:"active"`

	// Whether the organization has enabled communications
	Communications bool `json:"communications"`

	// Subscription details for the organization
	Subscription *SubscriptionDetails `json:"subscription"`

	// Usage counters for the organization
	Counters *SubscriptionUsage `json:"counters"`

	// Parent organization if this is a sub-organization
	Parent *OrganizationInfo `json:"parent"`
}

// OrganizationMembers represents a list of members of an organization.
// swagger:model OrganizationMembers
type OrganizationMembers struct {
	// List of organization members
	Members []*OrganizationMember `json:"members"`
}

// OrganizationMember represents a member of an organization with their role.
// swagger:model OrganizationMember
type OrganizationMember struct {
	// User information
	Info *UserInfo `json:"info"`

	// The role of the member in the organization
	Role string `json:"role"`
}

// OrganizationAddresses represents a list of blockchain addresses of organizations.
// swagger:model OrganizationAddresses
type OrganizationAddresses struct {
	// List of organization blockchain addresses
	Addresses []string `json:"addresses"`
}

// UserOrganization represents the organization of a user including their role.
// swagger:model UserOrganization
type UserOrganization struct {
	// The role of the user in the organization
	Role string `json:"role"`

	// Organization information
	Organization *OrganizationInfo `json:"organization"`
}

// OrganizationRole represents a role that can be assigned to organization members.
// swagger:model OrganizationRole
type OrganizationRole struct {
	// Role identifier
	Role string `json:"role"`

	// Human-readable name of the role
	Name string `json:"name"`

	// Whether this role has write permissions
	WritePermission bool `json:"writePermission"`
}

// OrganizationRoleList represents a list of organization roles.
// swagger:model OrganizationRoleList
type OrganizationRoleList struct {
	// List of organization roles
	Roles []*OrganizationRole `json:"roles"`
}

// OrganizationType represents a type of organization.
// swagger:model OrganizationType
type OrganizationType struct {
	// Type identifier
	Type string `json:"type"`

	// Human-readable name of the type
	Name string `json:"name"`
}

// OrganizationTypeList represents a list of organization types.
// swagger:model OrganizationTypeList
type OrganizationTypeList struct {
	// List of organization types
	Types []*OrganizationType `json:"types"`
}

// UserInfo represents user information and is used for user registration.
// swagger:model UserInfo
type UserInfo struct {
	// User's email address
	Email string `json:"email,omitempty"`

	// User's password (not returned in responses)
	Password string `json:"password,omitempty"`

	// User's first name
	FirstName string `json:"firstName,omitempty"`

	// User's last name
	LastName string `json:"lastName,omitempty"`

	// Whether the user's email is verified
	Verified bool `json:"verified,omitempty"`

	// Organizations the user belongs to
	Organizations []*UserOrganization `json:"organizations"`
}

// OrganizationInvite represents an invitation to join an organization.
// swagger:model OrganizationInvite
type OrganizationInvite struct {
	// Email address of the invitee
	Email string `json:"email"`

	// Role to be assigned to the invitee
	Role string `json:"role"`

	// Expiration time of the invitation
	Expiration time.Time `json:"expiration"`
}

// OrganizationInviteList represents a list of pending organization invitations.
// swagger:model OrganizationInviteList
type OrganizationInviteList struct {
	// List of pending invitations
	Invites []*OrganizationInvite `json:"pending"`
}

// AcceptOrganizationInvitation represents a request to accept an organization invitation.
// swagger:model AcceptOrganizationInvitation
type AcceptOrganizationInvitation struct {
	// Invitation code
	Code string `json:"code"`

	// User information for registration or identification
	User *UserInfo `json:"user"`
}

// UserPasswordUpdate represents a request to update a user's password.
// swagger:model UserPasswordUpdate
type UserPasswordUpdate struct {
	// Current password
	OldPassword string `json:"oldPassword"`

	// New password
	NewPassword string `json:"newPassword"`
}

// UserVerification represents user verification information.
// swagger:model UserVerification
type UserVerification struct {
	// User's email address
	Email string `json:"email,omitempty"`

	// Verification code
	Code string `json:"code,omitempty"`

	// Expiration time of the verification code
	Expiration time.Time `json:"expiration,omitempty"`

	// Whether the verification is valid
	Valid bool `json:"valid"`
}

// UserPasswordReset represents a request to reset a user's password.
// swagger:model UserPasswordReset
type UserPasswordReset struct {
	// User's email address
	Email string `json:"email"`

	// Password reset code
	Code string `json:"code"`

	// New password
	NewPassword string `json:"newPassword"`
}

// LoginResponse represents the response to a successful login request.
// swagger:model LoginResponse
type LoginResponse struct {
	// JWT authentication token
	Token string `json:"token"`

	// Token expiration time
	Expirity time.Time `json:"expirity"`
}

// TransactionData contains the data of a transaction to be signed or a signed transaction.
// swagger:model TransactionData
type TransactionData struct {
	// Blockchain address
	Address internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Transaction payload bytes
	TxPayload []byte `json:"txPayload" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// MessageSignature contains a payload and its signature.
// swagger:model MessageSignature
type MessageSignature struct {
	// Blockchain address
	Address string `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Message payload bytes
	Payload []byte `json:"payload,omitempty" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`

	// Cryptographic signature
	Signature internal.HexBytes `json:"signature,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// OrganizationFromDB converts a db.Organization to an OrganizationInfo, if the parent
// organization is provided it will be included in the response.
func OrganizationFromDB(dbOrg, parent *db.Organization) *OrganizationInfo {
	if dbOrg == nil {
		return nil
	}
	var parentOrg *OrganizationInfo
	if parent != nil {
		parentOrg = OrganizationFromDB(parent, nil)
	}
	details := SubscriptionDetailsFromDB(&dbOrg.Subscription)
	usage := SubscriptionUsageFromDB(&dbOrg.Counters)
	return &OrganizationInfo{
		Address:        dbOrg.Address,
		Website:        dbOrg.Website,
		CreatedAt:      dbOrg.CreatedAt.Format(time.RFC3339),
		Type:           string(dbOrg.Type),
		Size:           dbOrg.Size,
		Color:          dbOrg.Color,
		Subdomain:      dbOrg.Subdomain,
		Country:        dbOrg.Country,
		Timezone:       dbOrg.Timezone,
		Active:         dbOrg.Active,
		Communications: dbOrg.Communications,
		Parent:         parentOrg,
		Subscription:   &details,
		Counters:       &usage,
	}
}

// OrganizationSubscriptionInfo provides detailed information about an organization's subscription.
// swagger:model OrganizationSubscriptionInfo
type OrganizationSubscriptionInfo struct {
	// Subscription details
	SubcriptionDetails SubscriptionDetails `json:"subscriptionDetails"`

	// Current usage metrics
	Usage SubscriptionUsage `json:"usage"`

	// Subscription plan details
	Plan SubscriptionPlan `json:"plan"`
}

// SubscriptionPlan represents a subscription plan in the API.
// It is the mirror struct of db.Plan.
// swagger:model SubscriptionPlan
type SubscriptionPlan struct {
	// Unique identifier for the plan
	ID uint64 `json:"id"`

	// Human-readable name of the plan
	Name string `json:"name"`

	// Stripe product ID
	StripeID string `json:"stripeID"`

	// Stripe price ID
	StripePriceID string `json:"stripePriceID"`

	// Starting price in cents
	StartingPrice int64 `json:"startingPrice"`

	// Whether this is the default plan
	Default bool `json:"default"`

	// Organization limits for this plan
	Organization SubscriptionPlanLimits `json:"organization"`

	// Voting types available in this plan
	VotingTypes SubscriptionVotingTypes `json:"votingTypes"`

	// Features available in this plan
	Features SubscriptionFeatures `json:"features"`

	// Census size tiers and pricing
	CensusSizeTiers []SubscriptionPlanTier `json:"censusSizeTiers"`
}

// SubscriptionPlanFromDB converts a db.Plan to a SubscriptionPlan.
func SubscriptionPlanFromDB(plan *db.Plan) SubscriptionPlan {
	if plan == nil {
		return SubscriptionPlan{}
	}
	tiers := make([]SubscriptionPlanTier, 0, len(plan.CensusSizeTiers))
	for _, t := range plan.CensusSizeTiers {
		tiers = append(tiers, SubscriptionPlanTier{
			Amount: t.Amount,
			UpTo:   t.UpTo,
		})
	}
	return SubscriptionPlan{
		ID:              plan.ID,
		Name:            plan.Name,
		StripeID:        plan.StripeID,
		StripePriceID:   plan.StripePriceID,
		StartingPrice:   plan.StartingPrice,
		Default:         plan.Default,
		Organization:    SubscriptionPlanLimits(plan.Organization),
		VotingTypes:     SubscriptionVotingTypes(plan.VotingTypes),
		Features:        SubscriptionFeatures(plan.Features),
		CensusSizeTiers: tiers,
	}
}

// SubscriptionPlanLimits represents the limits of a subscription plan.
// It is the mirror struct of db.PlanLimits.
// swagger:model SubscriptionPlanLimits
type SubscriptionPlanLimits struct {
	// Maximum number of members allowed
	Members int `json:"members"`

	// Maximum number of sub-organizations allowed
	SubOrgs int `json:"subOrgs"`

	// Maximum number of voting processes allowed
	MaxProcesses int `json:"maxProcesses"`

	// Maximum number of census allowed
	MaxCensus int `json:"maxCensus"`

	// Maximum duration of voting processes in days
	MaxDuration int `json:"maxDuration"`

	// Whether custom URLs are allowed
	CustomURL bool `json:"customURL"`

	// Maximum number of draft processes allowed
	Drafts int `json:"drafts"`
}

// SubscriptionVotingTypes represents the voting types available in a subscription plan.
// It is the mirror struct of db.VotingTypes.
// swagger:model SubscriptionVotingTypes
type SubscriptionVotingTypes struct {
	// Whether single choice voting is available
	Single bool `json:"single"`

	// Whether multiple choice voting is available
	Multiple bool `json:"multiple"`

	// Whether approval voting is available
	Approval bool `json:"approval"`

	// Whether cumulative voting is available
	Cumulative bool `json:"cumulative"`

	// Whether ranked choice voting is available
	Ranked bool `json:"ranked"`

	// Whether weighted voting is available
	Weighted bool `json:"weighted"`
}

// SubscriptionFeatures represents the features available in a subscription plan.
// It is the mirror struct of db.Features.
// swagger:model SubscriptionFeatures
type SubscriptionFeatures struct {
	// Whether anonymous voting is available
	Anonymous bool `json:"anonymous"`

	// Whether census overwrite is allowed
	Overwrite bool `json:"overwrite"`

	// Whether live results are available
	LiveResults bool `json:"liveResults"`

	// Whether UI personalization is available
	Personalization bool `json:"personalization"`

	// Whether email reminders are available
	EmailReminder bool `json:"emailReminder"`

	// Whether SMS notifications are available
	SmsNotification bool `json:"smsNotification"`

	// Whether white labeling is available
	WhiteLabel bool `json:"whiteLabel"`

	// Whether live streaming is available
	LiveStreaming bool `json:"liveStreaming"`
}

// SubscriptionPlanTier represents a pricing tier of a subscription plan.
// It is the mirror struct of db.PlanTier.
// swagger:model SubscriptionPlanTier
type SubscriptionPlanTier struct {
	// Price amount in cents
	Amount int64 `json:"amount"`

	// Maximum census size for this tier
	UpTo int64 `json:"upTo"`
}

// SubscriptionDetails represents the details of an organization's subscription.
// It is the mirror struct of db.OrganizationSubscription.
// swagger:model SubscriptionDetails
type SubscriptionDetails struct {
	// ID of the subscription plan
	PlanID uint64 `json:"planID"`

	// Date when the subscription started
	StartDate time.Time `json:"startDate"`

	// Date when the subscription will renew
	RenewalDate time.Time `json:"renewalDate"`

	// Date of the last payment
	LastPaymentDate time.Time `json:"lastPaymentDate"`

	// Whether the subscription is active
	Active bool `json:"active"`

	// Maximum census size allowed
	MaxCensusSize int `json:"maxCensusSize"`

	// Email associated with the subscription
	Email string `json:"email"`
}

// SubscriptionDetailsFromDB converts a db.OrganizationSubscription to a SubscriptionDetails.
func SubscriptionDetailsFromDB(details *db.OrganizationSubscription) SubscriptionDetails {
	if details == nil {
		return SubscriptionDetails{}
	}
	return SubscriptionDetails{
		PlanID:          details.PlanID,
		StartDate:       details.StartDate,
		RenewalDate:     details.RenewalDate,
		LastPaymentDate: details.LastPaymentDate,
		Active:          details.Active,
		MaxCensusSize:   details.MaxCensusSize,
		Email:           details.Email,
	}
}

// SubscriptionUsage represents the usage metrics of an organization's subscription.
// It is the mirror struct of db.OrganizationCounters.
// swagger:model SubscriptionUsage
type SubscriptionUsage struct {
	// Number of SMS messages sent
	SentSMS int `json:"sentSMS"`

	// Number of emails sent
	SentEmails int `json:"sentEmails"`

	// Number of sub-organizations created
	SubOrgs int `json:"subOrgs"`

	// Number of members in the organization
	Members int `json:"members"`

	// Number of voting processes created
	Processes int `json:"processes"`
}

// SubscriptionUsageFromDB converts a db.OrganizationCounters to a SubscriptionUsage.
func SubscriptionUsageFromDB(usage *db.OrganizationCounters) SubscriptionUsage {
	if usage == nil {
		return SubscriptionUsage{}
	}
	return SubscriptionUsage{
		SentSMS:    usage.SentSMS,
		SentEmails: usage.SentEmails,
		SubOrgs:    usage.SubOrgs,
		Members:    usage.Members,
		Processes:  usage.Processes,
	}
}

// SubscriptionCheckout represents the details required for a subscription checkout process.
// swagger:model SubscriptionCheckout
type SubscriptionCheckout struct {
	// Plan lookup key
	LookupKey uint64 `json:"lookupKey"`

	// URL to return to after checkout
	ReturnURL string `json:"returnURL"`

	// Amount in cents
	Amount int64 `json:"amount"`

	// Organization address
	Address string `json:"address"`

	// Locale for the checkout page
	Locale string `json:"locale"`
}

// ParticipantNotification represents a notification sent to a participant.
// swagger:model ParticipantNotification
type ParticipantNotification struct {
	// ID of the voting process
	ProcessID []byte `json:"processID" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`

	// Notification details
	Notification notifications.Notification `json:"notification"`

	// Whether the notification was sent
	Sent bool `json:"sent"`

	// When the notification was sent
	SentAt time.Time `json:"sentAt"`
}

// OrganizationCensus represents a census of an organization.
// It is the mirror struct of db.Census.
// swagger:model OrganizationCensus
type OrganizationCensus struct {
	// Unique identifier for the census
	ID string `json:"censusID"`

	// Type of census
	Type db.CensusType `json:"type"`

	// Organization address
	OrgAddress string `json:"orgAddress"`
}

// OrganizationCensusFromDB converts a db.Census to an OrganizationCensus.
func OrganizationCensusFromDB(census *db.Census) OrganizationCensus {
	if census == nil {
		return OrganizationCensus{}
	}
	return OrganizationCensus{
		ID:         census.ID.Hex(),
		Type:       census.Type,
		OrgAddress: census.OrgAddress,
	}
}

// PublishedCensusResponse represents a published census.
// swagger:model PublishedCensusResponse
type PublishedCensusResponse struct {
	// URI of the published census
	URI string `json:"uri" bson:"uri"`

	// Merkle root of the census
	Root internal.HexBytes `json:"root" bson:"root" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Census ID
	CensusID internal.HexBytes `json:"censusId" bson:"censusId" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// OrganizationCensuses wraps a list of censuses of an organization.
// swagger:model OrganizationCensuses
type OrganizationCensuses struct {
	// List of organization censuses
	Censuses []OrganizationCensus `json:"censuses"`
}

// AddParticipantsRequest defines the payload for adding participants to a census.
// swagger:model AddParticipantsRequest
type AddParticipantsRequest struct {
	// List of participants to add
	Participants []OrgParticipant `json:"participants"`
}

// DbOrgParticipants converts the participants in the request to db.OrgParticipant objects.
func (r *AddParticipantsRequest) DbOrgParticipants(orgAddress string) []db.OrgParticipant {
	participants := make([]db.OrgParticipant, 0, len(r.Participants))
	for _, p := range r.Participants {
		participants = append(participants, p.ToDb(orgAddress))
	}
	return participants
}

// OrgParticipant defines the structure of a participant in the API.
// It is the mirror struct of db.OrgParticipant.
// swagger:model OrgParticipant
type OrgParticipant struct {
	// Unique participant number
	ParticipantNo string `json:"participantNo"`

	// Participant's name
	Name string `json:"name"`

	// Participant's email address
	Email string `json:"email"`

	// Participant's phone number
	Phone string `json:"phone"`

	// Participant's password (for authentication)
	Password string `json:"password"`

	// Additional custom fields
	Other map[string]any `json:"other"`
}

// ToDb converts an OrgParticipant to a db.OrgParticipant.
func (p *OrgParticipant) ToDb(orgAddress string) db.OrgParticipant {
	return db.OrgParticipant{
		OrgAddress:    orgAddress,
		ParticipantNo: p.ParticipantNo,
		Name:          p.Name,
		Email:         p.Email,
		Phone:         p.Phone,
		Password:      p.Password,
		Other:         p.Other,
	}
}

// AddParticipantsResponse defines the response for successful participant addition.
// swagger:model AddParticipantsResponse
type AddParticipantsResponse struct {
	// Number of participants added
	ParticipantsNo uint32 `json:"participantsNo"`

	// Job ID for tracking the addition process
	JobID internal.HexBytes `json:"jobID" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// Request types for process operations

// CreateProcessRequest defines the payload for creating a new voting process.
// swagger:model CreateProcessRequest
type CreateProcessRequest struct {
	// Merkle root of the published census
	PublishedCensusRoot internal.HexBytes `json:"censusRoot" swaggertype:"string" format:"hex" example:"deadbeef"`

	// URI of the published census
	PublishedCensusURI string `json:"censusUri"`

	// Census ID
	CensusID internal.HexBytes `json:"censusID" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Additional metadata for the process
	// Can be any key-value pairs
	Metadata []byte `json:"metadata,omitempty" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// InitiateAuthRequest defines the payload for participant authentication.
// swagger:model InitiateAuthRequest
type InitiateAuthRequest struct {
	// Unique participant number
	ParticipantNo string `json:"participantNo"`

	// Participant's email address (optional)
	Email string `json:"email,omitempty"`

	// Participant's phone number (optional)
	Phone string `json:"phone,omitempty"`

	// Participant's password (optional)
	Password string `json:"password,omitempty"`
}

// VerifyAuthRequest defines the payload for auth code verification.
// swagger:model VerifyAuthRequest
type VerifyAuthRequest struct {
	// Authentication token
	Token string `json:"token"`

	// Verification code
	Code string `json:"code"`
}

// GenerateProofRequest defines the payload for generating voting proof.
// swagger:model GenerateProofRequest
type GenerateProofRequest struct {
	// Authentication token
	Token string `json:"token"`

	// Blinded address for proof generation
	BlindedAddress []byte `json:"blindedAddress" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// Two-factor authentication types

// AuthRequest defines the payload for requesting authentication.
// swagger:model AuthRequest
type AuthRequest struct {
	// Authentication token
	AuthToken *uuid.UUID `json:"authToken,omitempty"`

	// Authentication data (reserved for the auth handler)
	AuthData []string `json:"authData,omitempty"`
}

// SignRequest defines the payload for requesting a signature.
// swagger:model SignRequest
type SignRequest struct {
	// Token R value
	TokenR internal.HexBytes `json:"tokenR" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Authentication token
	AuthToken *uuid.UUID `json:"authToken"`

	// Blockchain address
	Address string `json:"address,omitempty"`

	// Payload to sign
	Payload string `json:"payload,omitempty"`

	// Election ID
	ElectionID internal.HexBytes `json:"electionId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// CreateProcessBundleRequest defines the payload for creating a new process bundle.
// swagger:model CreateProcessBundleRequest
type CreateProcessBundleRequest struct {
	// Census ID
	CensusID string `json:"censusID"`

	// List of process IDs to include in the bundle
	Processes []string `json:"processes"`
}

// CreateProcessBundleResponse defines the response for a successful process bundle creation.
// swagger:model CreateProcessBundleResponse
type CreateProcessBundleResponse struct {
	// URI of the created process bundle
	URI string `json:"uri"`

	// Merkle root of the process bundle
	Root internal.HexBytes `json:"root" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// OauthLoginRequest defines the payload for register/login through the OAuth service.
// swagger:model OauthLoginRequest
type OauthLoginRequest struct {
	// User email address
	Email string `json:"email"`
	// User first name
	FirstName string `json:"firstName"`
	// User last name
	LastName string `json:"lastName"`
	// The signature made by the OAuth service on top of the user email
	OauthSignature string `json:"oauthSignature"`
	// The signature made by the user on on top of the oauth signature
	UserOauthSignature string `json:"userOauthSignature"`
	// The address of the user
	Address string `json:"address"`
}

// OauthServiceAddressResponse defines the response from the OAuth service containing its address.
type OauthServiceAddressResponse struct {
	// The address of the OAuth service signer
	Address string `json:"address"`
}
