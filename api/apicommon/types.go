package apicommon

//revive:disable:max-public-structs

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

// OrganizationInfo represents an organization in the API.
// swagger:model OrganizationInfo
type OrganizationInfo struct {
	// The organization's blockchain address
	Address common.Address `json:"address"`

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

	// Arbitrary key value fields with metadata regarding the organization
	Meta map[string]any `json:"meta"`
}

// OrganizationUsers represents a list of users of an organization.
// swagger:model OrganizationUsers
type OrganizationUsers struct {
	// List of organization users
	Users []*OrganizationUser `json:"users"`
}

// OrganizationUser represents a user of an organization with their role.
// swagger:model OrganizationUser
type OrganizationUser struct {
	// User information
	Info *UserInfo `json:"info"`

	// The role of the user in the organization
	Role string `json:"role"`
}

// OrganizationAddresses represents a list of blockchain addresses of organizations.
// swagger:model OrganizationAddresses
type OrganizationAddresses struct {
	// List of organization blockchain addresses
	Addresses []common.Address `json:"addresses"`
}

// UserOrganization represents the organization of a user including their role.
// swagger:model UserOrganization
type UserOrganization struct {
	// The role of the user in the organization
	Role string `json:"role"`

	// Organization information
	Organization *OrganizationInfo `json:"organization"`
}

// OrganizationRole represents a role that can be assigned to organization users.
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

// OrganizationAddMetaRequest represents a request to add or update meta information for an organization.
// swagger:model OrganizationAddMetaRequest
type OrganizationAddMetaRequest struct {
	// Set of key-value pairs to add or update in the organization's meta information
	Meta map[string]any `json:"meta"`
}

// OrganizationMetaResponse represents the meta information of an organization.
// swagger:model OrganizationMetaResponse
type OrganizationMetaResponse struct {
	// Meta information of the organization
	Meta map[string]any `json:"meta"`
}

// OrganizationDeleteMetaRequest represents a request to delete a set of keys from the meta information
// for an organization.
// swagger:model OrganizationDeleteMetaRequest
type OrganizationDeleteMetaRequest struct {
	// List of keys to delete from the organization's meta information
	Keys []string `json:"keys"`
}

// UpdateOrganizationUserRoleRequest represents a request to update the role of an organization user.
// swagger:model UpdateOrganizationUserRoleRequest
type UpdateOrganizationUserRoleRequest struct {
	// The new role to assign to the user
	Role string `json:"role"`
}

// CreateOrganizationMemberGroupRequest represents a request to create a new organization member group.
// swagger:model CreateOrganizationMemberGroupRequest
type CreateOrganizationMemberGroupRequest struct {
	// Title of the group
	Title string `json:"title"`
	// Description of the group
	Description string `json:"description"`
	// The IDs of the members to add to the group
	MemberIDs []internal.ObjectID `json:"memberIds"`
}

// OrganizationMemberGroupInfo represents detailed information about an organization member group.
// swagger:model OrganizationMemberGroupInfo
type OrganizationMemberGroupInfo struct {
	// Unique identifier for the group
	ID internal.ObjectID `json:"id,omitempty" bson:"_id"`
	// Title of the group
	Title string `json:"title,omitempty" bson:"title"`
	// Description of the group
	Description string `json:"description,omitempty" bson:"description"`
	// Creation timestamp
	CreatedAt time.Time `json:"createdAt,omitempty" bson:"createdAt"`
	// Last updated timestamp
	UpdatedAt time.Time `json:"updatedAt,omitempty" bson:"updatedAt"`
	// List of member IDs in the group
	MemberIDs []internal.ObjectID `json:"memberIds,omitempty" bson:"memberIds"`
	// List of census IDs associated with the group
	CensusIDs []internal.ObjectID `json:"censusIds,omitempty" bson:"censusIds"`
	// Count of members in the group
	MembersCount int `json:"membersCount,omitempty" bson:"membersCount"`
}

// OrganizationMemberGroupsResponse represents the response for listing organization member groups.
// swagger:model OrganizationMemberGroupsResponse
type OrganizationMemberGroupsResponse struct {
	// Total number of pages
	TotalPages int `json:"totalPages"`
	// Current page number
	CurrentPage int `json:"currentPage"`
	// List of organization member groups
	Groups []*OrganizationMemberGroupInfo `json:"groups"`
}

// UpdateOrganizationMemberGroupsRequest represents a request to update an organization member group
// title, description or members.
// swagger:model UpdateOrganizationMemberGroupsRequest
type UpdateOrganizationMemberGroupsRequest struct {
	// Updated Title
	Title string `json:"title"`
	// Updated Description
	Description string `json:"description"`
	// The IDs of the members to add to the group
	AddMembers []internal.ObjectID `json:"addMembers"`
	// The IDs of the members to remove from the group
	RemoveMembers []internal.ObjectID `json:"removeMembers"`
}

// ListOrganizationMemberGroupResponse represents the response for listing the members of an  organization group.
// swagger:model ListOrganizationMemberGroupResponse
type ListOrganizationMemberGroupResponse struct {
	// Total number of pages
	TotalPages int `json:"totalPages"`
	// Current page number
	CurrentPage int `json:"currentPage"`
	// List of organization group members
	Members []OrgMember `json:"members"`
}

// ValidateMemberGroupRequest validates the request for creating or updating an organization member group.
// Validates that either AuthFields or TwoFaFields are provided and checks for duplicates or empty fields.
// swagger:model ValidateMemberGroupRequest
type ValidateMemberGroupRequest struct {
	// Defines which member data should be used for authentication
	AuthFields db.OrgMemberAuthFields `json:"authFields,omitempty"`

	// Defines which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
}

// UserInfo represents user information and is used for user registration.
// swagger:model UserInfo
type UserInfo struct {
	// User ID as generated by the backend
	ID uint64 `json:"id,omitempty"`
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
	// Unique identifier for the invitation
	ID internal.ObjectID `json:"id"`

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
	Address common.Address `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Transaction payload bytes
	TxPayload []byte `json:"txPayload" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// MessageSignature contains a payload and its signature.
// swagger:model MessageSignature
type MessageSignature struct {
	// Blockchain address
	Address common.Address `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

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
	StripeID string `json:"stripeId"`

	// Stripe price ID
	StripePriceID string `json:"stripePriceId"`

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
	// Maximum number of users allowed
	Users int `json:"users"`

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

	// Whether eligible for phone support
	PhoneSupport bool `json:"phoneSupport"`
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
	PlanID uint64 `json:"planId"`

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

	// Number of users in the organization
	Users int `json:"users"`

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
		Users:      usage.Users,
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
	Address common.Address `json:"address"`

	// Locale for the checkout page
	Locale string `json:"locale"`
}

// MemberNotification represents a notification sent to a member.
// swagger:model MemberNotification
type MemberNotification struct {
	// ID of the voting process
	ProcessID []byte `json:"processId" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`

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
	ID internal.ObjectID `json:"censusId"`

	// Type of census
	Type db.CensusType `json:"type"`

	// Organization address
	OrgAddress common.Address `json:"orgAddress"`

	// Size of the census
	Size int64 `json:"size"`

	// Optional for creating a census based on an organization member group
	GroupID internal.ObjectID `json:"groupID,omitempty"`

	// Optional for defining which member data should be used for authentication
	AuthFields db.OrgMemberAuthFields `json:"authFields,omitempty"`

	// Optional for defining which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
}

// CreateCensusRequest represents a request to create a new census for an organization.
// swagger:model CreateCensusRequest
type CreateCensusRequest struct {
	// Organization address
	OrgAddress common.Address `json:"orgAddress"`

	// Optional for defining which member data should be used for authentication
	AuthFields db.OrgMemberAuthFields `json:"authFields,omitempty"`

	// Optional for defining which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
}

// CreateCensusResponse represents the response after creating a census returning the census ID.
// swagger:model CreateCensusResponse
type CreateCensusResponse struct {
	// Unique identifier for the census
	ID internal.ObjectID `json:"id,omitempty"`
}

// PublishedCensusResponse represents a published census.
// swagger:model PublishedCensusResponse
type PublishedCensusResponse struct {
	// URI of the published census
	URI string `json:"uri" bson:"uri"`

	// Merkle root of the census
	Root internal.HexBytes `json:"root" bson:"root" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Size of the published census
	Size int64 `json:"size"`
}

// PublishCensusGroupRequest represents a request to publish a census group.
// swagger:model PublishCensusGroupRequest
type PublishCensusGroupRequest struct {
	// Optional for defining which member data should be used for authentication
	AuthFields db.OrgMemberAuthFields `json:"authFields,omitempty"`

	// Optional for defining which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
}

// CensusParticipantsResponse returns the memberIDs of the participants of a census.
// swagger:model CensusParticipantsResponse
type CensusParticipantsResponse struct {
	// Unique identifier for the census
	CensusID internal.ObjectID `json:"censusId"`
	// List of member IDs of the participants
	MemberIDs []internal.ObjectID `json:"memberIds"`
}

// OrganizationCensusFromDB converts a db.Census to an OrganizationCensus.
func OrganizationCensusFromDB(census *db.Census) OrganizationCensus {
	if census == nil {
		return OrganizationCensus{}
	}
	return OrganizationCensus{
		ID:          census.ID,
		Type:        census.Type,
		OrgAddress:  census.OrgAddress,
		Size:        census.Size,
		GroupID:     census.GroupID,
		AuthFields:  census.AuthFields,
		TwoFaFields: census.TwoFaFields,
	}
}

// OrganizationCensuses wraps a list of censuses of an organization.
// swagger:model OrganizationCensuses
type OrganizationCensuses struct {
	// List of organization censuses
	Censuses []OrganizationCensus `json:"censuses"`
}

// AddMembersRequest defines the payload for adding members to a census.
// swagger:model AddMembersRequest
type AddMembersRequest struct {
	// List of members to add
	Members []OrgMember `json:"members"`
}

// DbOrgMembers converts the members in the request to db.OrgMember objects.
func (r *AddMembersRequest) DbOrgMembers(orgAddress common.Address) []db.OrgMember {
	members := make([]db.OrgMember, 0, len(r.Members))
	for _, p := range r.Members {
		members = append(members, p.ToDb(orgAddress))
	}
	return members
}

type DeleteMembersRequest struct {
	// List of member internal ids numbers to delete
	IDs []internal.ObjectID `json:"ids"`
}

type DeleteMembersResponse struct {
	// Number of members deleted
	Count int `json:"count"`
}

// OrgMember defines the structure of a member in the API.
// It is the mirror struct of db.OrgMember.
// swagger:model OrgMember
type OrgMember struct {
	// Member's internal unique internal ID
	ID internal.ObjectID `json:"id"`

	// Unique member number as defined by the organization
	MemberNumber string `json:"memberNumber"`

	// Member's name
	Name string `json:"name,omitempty"`

	// Member's surname
	Surname string `json:"surname,omitempty"`

	// Member's National ID No
	NationalID string `json:"nationalId,omitempty"`

	// Member's date of birth in format YYYY-MM-DD
	BirthDate string `json:"birthDate,omitempty"`

	// Member's email address
	Email string `json:"email,omitempty"`

	// Member's phone number
	Phone string `json:"phone,omitempty"`

	// Member's password (for authentication)
	Password string `json:"password,omitempty"`

	// Additional custom fields
	Other map[string]any `json:"other"`
}

// ToDb converts an OrgMember to a db.OrgMember.
func (p *OrgMember) ToDb(orgAddress common.Address) db.OrgMember {
	parsedBirthDate := time.Time{}
	if len(p.BirthDate) > 0 {
		// Parse the birth date from string to time.Time
		var err error
		parsedBirthDate, err = time.Parse("2006-01-02", p.BirthDate)
		if err != nil {
			log.Warnf("Failed to parse birth date %s for member %s: %v", p.BirthDate, p.MemberNumber, err)
		}
	}
	return db.OrgMember{
		ID:             p.ID,
		OrgAddress:     orgAddress,
		MemberNumber:   p.MemberNumber,
		Name:           p.Name,
		Surname:        p.Surname,
		NationalID:     p.NationalID,
		BirthDate:      p.BirthDate,
		ParsedBirtDate: parsedBirthDate,
		Email:          p.Email,
		Phone:          p.Phone,
		Password:       p.Password,
		Other:          p.Other,
	}
}

func OrgMemberFromDb(p db.OrgMember) OrgMember {
	hashedPhone := string(p.HashedPhone)
	if len(hashedPhone) > 0 {
		// If the phone is hashed, we return the last 6 characters
		hashedPhone = hashedPhone[len(hashedPhone)-6:]
	}
	// if p.BirthDate != nil {

	return OrgMember{
		ID:           p.ID,
		MemberNumber: p.MemberNumber,
		Name:         p.Name,
		Surname:      p.Surname,
		NationalID:   p.NationalID,
		BirthDate:    p.BirthDate,
		Email:        p.Email,
		Phone:        hashedPhone,
		Other:        p.Other,
	}
}

type OrganizationMembersResponse struct {
	// Total number of pages available
	Pages int `json:"pages"`

	// Current page number
	Page int `json:"page"`

	// Total number of members in the organization
	Members []OrgMember `json:"members"`
}

// AddMembersResponse defines the response for successful member addition
// swagger:model AddMembersResponse
type AddMembersResponse struct {
	// Number of members added
	Added uint32 `json:"added"`

	// Errors encountered during job
	Errors []string `json:"errors"`

	// Job ID for tracking the addition process
	JobID internal.HexBytes `json:"jobId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// AddMembersJobResponse defines the response for the status of an async job of member addition
// swagger:model AddMembersJobResponse
type AddMembersJobResponse struct {
	// Number of members added
	Added uint32 `json:"added"`

	// Errors encountered during job
	Errors []string `json:"errors"`

	// Progress equals Added / Total * 100
	Progress uint32 `json:"progress"`

	// Total members in this job
	Total uint32 `json:"total"`
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
	CensusID internal.ObjectID `json:"censusId" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Additional metadata for the process
	// Can be any key-value pairs
	Metadata []byte `json:"metadata,omitempty" swaggertype:"string" format:"base64" example:"aGVsbG8gd29ybGQ="`
}

// InitiateAuthRequest defines the payload for participant authentication.
// swagger:model InitiateAuthRequest
type InitiateAuthRequest struct {
	// Unique participant ID
	ParticipantID internal.ObjectID `json:"participantId"`

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
	CensusID internal.ObjectID `json:"censusId"`

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

// OAuthLoginRequest defines the payload for register/login through the OAuth service.
// swagger:model OAuthLoginRequest
type OAuthLoginRequest struct {
	// User email address
	Email string `json:"email"`
	// User first name
	FirstName string `json:"firstName"`
	// User last name
	LastName string `json:"lastName"`
	// The signature made by the OAuth service on top of the user email
	OAuthSignature string `json:"oauthSignature"`
	// The signature made by the user on on top of the oauth signature
	UserOAuthSignature string `json:"userOAuthSignature"`
	// The address of the user
	Address string `json:"address"`
}

type OAuthLoginResponse struct {
	// JWT authentication token
	Token string `json:"token"`

	// Token expiration time
	Expirity time.Time `json:"expirity"`

	// Whether the user had to be  registered
	Registered bool `json:"registered"`
}

// OAuthServiceAddressResponse defines the response from the OAuth service containing its address.
type OAuthServiceAddressResponse struct {
	// The address of the OAuth service signer
	Address string `json:"address"`
}

type CreateOrganizationTicketRequest struct {
	// Type of the ticket to create (definded externally)
	TicketType string `json:"type"`

	// Title of the ticket
	Title string `json:"title"`

	// Body of the ticket
	Description string `json:"description"`
}
