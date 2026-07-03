package apicommon

//revive:disable:max-public-structs

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
)

const (
	// DefaultItemsPerPage defines how many items per page are returned by the paginated endpoints,
	// when the client doesn't specify a `limit` param
	DefaultItemsPerPage = 10
	// MaxItemsPerPage defines a ceiling for the `limit` param passed by the client
	MaxItemsPerPage = 100
)

// MultilingualText is a locale-keyed string map. Clients may send either a plain string
// (normalised to {"default": "<string>"}) or an object {"<lang>": "<text>", ...}.
// When sending an object, a "default" key is required.
type MultilingualText map[string]string

// UnmarshalJSON implements json.Unmarshaler.
func (m *MultilingualText) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*m = MultilingualText{"default": s}
		return nil
	}
	var obj map[string]string
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("must be a string or an object with string values")
	}
	if _, ok := obj["default"]; !ok {
		return fmt.Errorf("multilingual object must have a \"default\" key")
	}
	*m = MultilingualText(obj)
	return nil
}

// multilingualFromAny extracts a MultilingualText from a meta map value. It handles both
// the in-memory form (MultilingualText / map[string]string, set at creation time) and the
// BSON-decoded form (map[string]interface{}, returned after a MongoDB round-trip).
func multilingualFromAny(v any) *MultilingualText {
	switch m := v.(type) {
	case MultilingualText:
		return &m
	case map[string]string:
		r := MultilingualText(m)
		return &r
	case map[string]any:
		r := make(MultilingualText, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				return nil
			}
			r[k] = s
		}
		return &r
	}
	return nil
}

// BuildOrgMeta merges the convenience name/logo/description fields with an explicit meta
// map. The explicit meta keys take precedence: if both name and meta["name"] are set,
// meta["name"] wins.
func BuildOrgMeta(name, logo, description *MultilingualText, explicit map[string]any) map[string]any {
	meta := make(map[string]any)
	if name != nil {
		meta["name"] = *name
	}
	if logo != nil {
		meta["logo"] = *logo
	}
	if description != nil {
		meta["description"] = *description
	}
	maps.Copy(meta, explicit)
	return meta
}

// Pagination contains all the values needed for the UI to easily organize the returned data
type Pagination struct {
	TotalItems   int64  `json:"totalItems"`
	PreviousPage *int64 `json:"previousPage"`
	CurrentPage  int64  `json:"currentPage"`
	NextPage     *int64 `json:"nextPage"`
	LastPage     int64  `json:"lastPage"`
}

// PaginationParams allows the client to request a specific page, and how many items per page
type PaginationParams struct {
	Page  int64 `json:"page,omitempty"`
	Limit int64 `json:"limit,omitempty"`
}

// ProcessInfo is the voter-facing response for a single voting process. It embeds the
// stored process and adds the Vocdoni chain ID the process lives on.
type ProcessInfo struct {
	*db.Process
	ChainID string `json:"chainId"`
}

// ProcessBundleInfo is the voter-facing response for a process bundle. It embeds the
// stored bundle and adds the Vocdoni chain ID the bundle's processes live on.
type ProcessBundleInfo struct {
	*db.ProcessesBundle
	ChainID string `json:"chainId"`
}

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

	// Integrator organization that manages this org, when it was created through
	// the integrator portal. Nil for regular organizations. Lets clients tell
	// managed orgs apart from the user's own orgs (e.g. to hide them from the org
	// switcher), a distinction Parent does not capture (managed orgs set ManagedBy,
	// not Parent).
	//
	// A pointer so it is omitted rather than emitted as the zero address: json's
	// omitempty does not skip the fixed-size common.Address array.
	ManagedBy *common.Address `json:"managedBy,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Arbitrary key value fields with metadata regarding the organization
	Meta map[string]any `json:"meta"`

	// Name is a shorthand for meta["name"]. On write accepts a plain string
	// (stored as {"default": "<string>"}) or a locale map; on read it mirrors
	// whatever is stored in meta["name"]. If both Name and meta["name"] are
	// provided on a create request, meta["name"] takes precedence.
	Name *MultilingualText `json:"name,omitempty"`

	// Logo is a shorthand for meta["logo"]. Same encoding rules as Name.
	Logo *MultilingualText `json:"logo,omitempty"`

	// Description is a shorthand for meta["description"]. Same encoding rules as Name.
	Description *MultilingualText `json:"description,omitempty"`

	// Whether to subscribe the new organization to the free integrator plan at
	// creation time (opt-in). Used by the integrator portal so a newly created org
	// becomes an integrator on the free tier with no checkout. Default false uses
	// the regular default plan.
	Integrator bool `json:"integrator,omitempty"`
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

	// Whether this organization is enabled as an integrator. Computed from the
	// organization's integrator limits / active plan (see Subscriptions.IsIntegrator),
	// not stored. Populated by GET /users/me; omitted when false.
	IsIntegrator bool `json:"isIntegrator,omitempty"`
}

// OrganizationRole represents a role that can be assigned to organization users.
// swagger:model OrganizationRole
type OrganizationRole struct {
	// Role identifier
	Role string `json:"role"`

	// Human-readable name of the role
	Name string `json:"name"`

	// Whether this role has organization write permission
	OrganizationWritePermission bool `json:"organizationWritePermission"`

	// Whether this role has process write permission
	ProcessWritePermission bool `json:"processWritePermission"`
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
	// The IDs of the members to add to the group (optional if IncludeAllMembers is true)
	MemberIDs []string `json:"memberIds,omitempty"`
	// Include all members of the organization in the group
	IncludeAllMembers bool `json:"includeAllMembers,omitempty"`
}

// OrganizationMemberGroupInfo represents detailed information about an organization member group.
// swagger:model OrganizationMemberGroupInfo
type OrganizationMemberGroupInfo struct {
	// Unique identifier for the group
	ID string `json:"id,omitempty" bson:"_id"`
	// Title of the group
	Title string `json:"title,omitempty" bson:"title"`
	// Description of the group
	Description string `json:"description,omitempty" bson:"description"`
	// Creation timestamp
	CreatedAt time.Time `json:"createdAt,omitempty" bson:"createdAt"`
	// Last updated timestamp
	UpdatedAt time.Time `json:"updatedAt,omitempty" bson:"updatedAt"`
	// List of member IDs in the group
	MemberIDs []string `json:"memberIds,omitempty" bson:"memberIds"`
	// List of census IDs associated with the group
	CensusIDs []string `json:"censusIds,omitempty" bson:"censusIds"`
	// Count of members in the group
	MembersCount int `json:"membersCount,omitempty" bson:"membersCount"`
	// IsAutoGroup indicates this is the auto-generated "All members" group.
	// It cannot be deleted and its membership cannot be manually modified.
	IsAutoGroup bool `json:"isAutoGroup,omitempty" bson:"isAutoGroup"`
}

// OrganizationMemberGroupsResponse represents the response for listing organization member groups.
// swagger:model OrganizationMemberGroupsResponse
type OrganizationMemberGroupsResponse struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
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
	AddMembers []string `json:"addMembers"`
	// The IDs of the members to remove from the group
	RemoveMembers []string `json:"removeMembers"`
}

// ListOrganizationMemberGroupResponse represents the response for listing the members of an  organization group.
// swagger:model ListOrganizationMemberGroupResponse
type ListOrganizationMemberGroupResponse struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
	// List of organization group members
	Members []OrgMember `json:"members"`
}

// ListOrganizationProcesses represents the response for listing the processes of an organization.
// swagger:model ListOrganizationProcesses
type ListOrganizationProcesses struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
	// List of organization processes
	Processes []db.Process `json:"processes"`
}

// OrganizationBundle represents an organization bundle. It contains the bundle ID and the main process ID.
// swagger:model OrganizationBundle
type OrganizationBundle struct {
	// The ID of the bundle
	BundleID string `json:"bundleId"`
	// The ID of the primary process which identifies the set of processes in
	// the bundle, no matter how many (one or more)
	PrimaryProcessID string `json:"primaryProcessId"`
	// The list of processes IDs in the bundle
	Processes []string `json:"processes"`
}

// ListOrganizationBundles represents the response for listing the bundles of an organization.
// swagger:model ListOrganizationBundles
type ListOrganizationBundles struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
	// List of organization bundles
	Bundles []OrganizationBundle `json:"bundles"`
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

	// Whether the user has a password set (true if not OAuth-only)
	HasPassword bool `json:"hasPassword"`

	// List of OAuth providers linked to this account (e.g., ["google", "github"])
	Providers []string `json:"providers"`

	// Organizations the user belongs to
	Organizations []*UserOrganization `json:"organizations"`
}

// OrganizationInvite represents an invitation to join an organization.
// swagger:model OrganizationInvite
type OrganizationInvite struct {
	// Unique identifier for the invitation
	ID string `json:"id"`

	// Email address of the invitee
	Email string `json:"email"`

	// Role to be assigned to the invitee
	Role db.UserRole `json:"role"`

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
	// normalize a nil Meta to an empty map so responses are consistent: the DB
	// read path already does this, but dbOrg may be built in-memory (e.g. at
	// creation) where Meta is nil, which would otherwise emit "meta": null.
	meta := dbOrg.Meta
	if meta == nil {
		meta = make(map[string]any)
	}
	// Expose ManagedBy only when set, as a pointer, so regular orgs omit the field
	// instead of serializing the zero address.
	var managedBy *common.Address
	if dbOrg.ManagedBy != (common.Address{}) {
		mb := dbOrg.ManagedBy
		managedBy = &mb
	}
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
		Meta:           meta,
		Name:           multilingualFromAny(meta["name"]),
		Logo:           multilingualFromAny(meta["logo"]),
		Description:    multilingualFromAny(meta["description"]),
		Parent:         parentOrg,
		ManagedBy:      managedBy,
		Subscription:   &details,
		Counters:       &usage,
	}
}

// CreateOrganizationRequest is the body of POST /organizations. It embeds the new
// organization's fields and adds creation-only directives that are not part of the
// organization's persistent representation (so they must not appear in responses).
// swagger:model CreateOrganizationRequest
type CreateOrganizationRequest struct {
	OrganizationInfo
	// Whether to provision the organization's on-chain account at creation
	// time (opt-in, eager). Default false preserves the legacy two-step flow
	// where the account is created later by the SDK.
	ProvisionAccount bool `json:"provisionAccount,omitempty"`
}

// CreateManagedOrganizationRequest is the body of POST /organizations/{address}/managed.
// It carries the new organization's fields plus an optional owner to assign as its admin.
type CreateManagedOrganizationRequest struct {
	OrganizationInfo
	// OwnerEmail optionally assigns an existing user as the managed org's creator/admin.
	OwnerEmail string `json:"ownerEmail,omitempty"`
}

// ListManagedOrganizations is the paginated list of organizations managed by an integrator.
type ListManagedOrganizations struct {
	Pagination    *Pagination         `json:"pagination"`
	Organizations []*OrganizationInfo `json:"organizations"`
}

// DeleteManagedOrganizationResponse is returned by DELETE
// /organizations/{address}/managed/{orgAddress} confirming the deleted address.
type DeleteManagedOrganizationResponse struct {
	Address string `json:"address"`
}

// IntegratorUsage holds an integrator's current managed-resource usage counters. SentVotes/
// SentSMS/SentEmails are the shared-pool totals summed across the integrator's managed orgs.
type IntegratorUsage struct {
	ManagedOrgs      int `json:"managedOrgs"`
	ManagedProcesses int `json:"managedProcesses"`
	SentVotes        int `json:"sentVotes"`
	SentSMS          int `json:"sentSMS"`
	SentEmails       int `json:"sentEmails"`
}

// IntegratorLimits holds an integrator's effective caps for the dashboard. MaxManagedOrgs is
// the effective integrator limit; the rest are the integrator's subscription-plan caps for the
// pools shared across its managed orgs.
//
// Zero is not uniformly "unlimited". Only MaxVotes treats 0 as unlimited (vote enforcement is
// skipped when the plan's MaxVotes is 0). MaxManagedProcesses, MaxSMS and MaxEmails are hard
// caps where 0 means no allowance. Separately, the plan-sourced fields (everything except
// MaxManagedOrgs, which always comes from the effective integrator limit) are left at 0 when an
// override-enabled integrator has no subscription plan to source caps from — an "unknown" the
// dashboard should treat distinctly from a real 0 cap.
type IntegratorLimits struct {
	MaxManagedOrgs      int `json:"maxManagedOrgs"`
	MaxManagedProcesses int `json:"maxManagedProcesses"`
	MaxVotes            int `json:"maxVotes"`
	MaxSMS              int `json:"maxSMS"`
	MaxEmails           int `json:"maxEmails"`
}

// IntegratorInfoResponse is returned by GET /organizations/{address}/integrator.
// Limits is only present when Enabled is true.
type IntegratorInfoResponse struct {
	Enabled bool              `json:"enabled"`
	Limits  *IntegratorLimits `json:"limits,omitempty"`
	Usage   IntegratorUsage   `json:"usage"`
}

// OrganizationSubscriptionInfo provides detailed information about an organization's subscription.
// swagger:model OrganizationSubscriptionInfo
type OrganizationSubscriptionInfo struct {
	// Subscription details
	SubscriptionDetails SubscriptionDetails `json:"subscriptionDetails"`

	// Current usage metrics
	Usage SubscriptionUsage `json:"usage"`

	// Subscription plan details
	Plan SubscriptionPlan `json:"plan"`
}

// SubscriptionPlan represents a subscription plan in the API.
// It is the mirror struct of db.Plan.
// swagger:model SubscriptionPlan
type SubscriptionPlan struct {
	// Unique identifier for the plan (its Stripe product ID)
	ID string `json:"id"`

	// Human-readable name of the plan
	Name string `json:"name"`

	// Stripe monthly price ID
	StripeMonthlyPriceID string `json:"stripeMonthlyPriceId"`

	// Monthly price
	MonthlyPrice int64 `json:"monthlyPrice"`

	// Stripe yearly price ID
	StripeYearlyPriceID string `json:"stripeYearlyPriceId"`

	// Yearly price
	YearlyPrice int64 `json:"yearlyPrice"`

	// Whether this is the default plan
	Default bool `json:"default"`

	// Organization limits for this plan
	Organization SubscriptionPlanLimits `json:"organization"`

	// Voting types available in this plan
	VotingTypes SubscriptionVotingTypes `json:"votingTypes"`

	// Features available in this plan
	Features SubscriptionFeatures `json:"features"`

	// Integrator limits for this plan (zero when the plan is not an integrator plan)
	IntegratorLimits SubscriptionIntegratorLimits `json:"integratorLimits"`
}

// SubscriptionPlanFromDB converts a db.Plan to a SubscriptionPlan.
func SubscriptionPlanFromDB(plan *db.Plan) SubscriptionPlan {
	if plan == nil {
		return SubscriptionPlan{}
	}
	return SubscriptionPlan{
		ID:                   plan.ID,
		Name:                 plan.Name,
		StripeMonthlyPriceID: plan.StripeMonthlyPriceID,
		MonthlyPrice:         plan.MonthlyPrice,
		StripeYearlyPriceID:  plan.StripeYearlyPriceID,
		YearlyPrice:          plan.YearlyPrice,
		Default:              plan.Default,
		Organization:         SubscriptionPlanLimits(plan.Organization),
		VotingTypes:          SubscriptionVotingTypes(plan.VotingTypes),
		Features:             SubscriptionFeatures(plan.Features),
		IntegratorLimits:     SubscriptionIntegratorLimits(plan.IntegratorLimits),
	}
}

// SubscriptionPlanLimits represents the limits of a subscription plan.
// It is the mirror struct of db.PlanLimits.
// swagger:model SubscriptionPlanLimits
type SubscriptionPlanLimits struct {
	// Maximum number of users allowed
	Users int `json:"teamMembers"`

	// Maximum number of sub-organizations allowed
	SubOrgs int `json:"subOrgs"`

	// Maximum number of voting processes allowed
	MaxProcesses int `json:"maxProcesses"`

	// Maximum number of census allowed
	MaxCensus int `json:"maxCensus"`

	// Maximum number of votes that may be relayed; 0 means unlimited
	MaxVotes int `json:"maxVotes"`

	// Maximum duration of voting processes in days
	MaxDuration int `json:"maxDaysDuration"`

	// Whether custom URLs are allowed
	CustomURL bool `json:"customURL"`

	// How many draft processes are allowed
	MaxDrafts int `json:"drafts"`

	// Whether this is a custom plan
	CustomPlan bool `json:"customPlan"`
}

// SubscriptionIntegratorLimits represents the integrator limits of a subscription plan.
// It is the mirror struct of db.IntegratorLimits. All-zero means the plan is not an
// integrator plan.
// swagger:model SubscriptionIntegratorLimits
type SubscriptionIntegratorLimits struct {
	// Maximum number of organizations the integrator may manage. The aggregate
	// process and census-size caps across managed orgs come from the plan's
	// top-level limits (maxProcesses / maxCensus).
	MaxManagedOrgs int `json:"maxManagedOrgs"`
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

	// Two-factor authentication sms limit
	TwoFaSms int `json:"2FAsms"`

	// Two-factor authentication email limit
	TwoFaEmail int `json:"2FAemail"`

	// Whether white labeling is available
	WhiteLabel bool `json:"whiteLabel"`

	// Whether live streaming is available
	LiveStreaming bool `json:"liveStreaming"`

	// Whether eligible for phone support
	PhoneSupport bool `json:"phoneSupport"`
}

// SubscriptionDetails represents the details of an organization's subscription.
// It is the mirror struct of db.OrganizationSubscription.
// swagger:model SubscriptionDetails
type SubscriptionDetails struct {
	// ID of the subscription plan (its Stripe product ID)
	PlanID string `json:"planId"`

	// Date when the subscription started
	StartDate time.Time `json:"startDate"`

	// Date when the subscription will renew
	RenewalDate time.Time `json:"renewalDate"`

	// Date of the last payment
	LastPaymentDate time.Time `json:"lastPaymentDate"`

	// Whether the subscription is active
	Active bool `json:"active"`

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

	// Number of votes relayed
	SentVotes int `json:"sentVotes"`

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
		SentVotes:  usage.SentVotes,
		SubOrgs:    usage.SubOrgs,
		Users:      usage.Users,
		Processes:  usage.Processes,
	}
}

// SubscriptionCheckout represents the details required for a subscription checkout process.
// swagger:model SubscriptionCheckout
type SubscriptionCheckout struct {
	// Plan lookup key (the plan's Stripe product ID)
	LookupKey string `json:"lookupKey"`

	// Billing period (e.g., "month" or "year")
	BillingPeriod string `json:"billingPeriod"`

	// URL to return to after checkout
	ReturnURL string `json:"returnURL"`

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
	ID string `json:"censusId"`

	// Type of census
	Type db.CensusType `json:"type"`

	// Organization address
	OrgAddress common.Address `json:"orgAddress"`

	// Size of the census
	Size int64 `json:"size"`

	// Weighted indicates if the census uses weighted voting
	Weighted bool `json:"weighted"`

	// Optional for creating a census based on an organization member group
	GroupID string `json:"groupID,omitempty"`

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
	ID string `json:"id,omitempty"`
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
	// Member data fields used for authentication (e.g. nationalId, birthDate). At least one
	// of authFields or twoFaFields must be provided; supplying only authFields publishes an
	// auth-only (no OTP) CSP census. A request with both empty is rejected (ErrCensusTypeNotFound).
	AuthFields db.OrgMemberAuthFields `json:"authFields,omitempty"`

	// Member data fields used for two-factor authentication (email and/or phone, sent as an OTP
	// challenge). At least one of authFields or twoFaFields must be provided.
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`

	// Indicates if the census is weighted
	Weighted bool `json:"weighted,omitempty"`
}

// CensusParticipantsResponse returns the memberIDs of the participants of a census.
// swagger:model CensusParticipantsResponse
type CensusParticipantsResponse struct {
	// Unique identifier for the census
	CensusID string `json:"censusId"`
	// List of member IDs of the participants
	MemberIDs []string `json:"memberIds"`
}

// OrganizationCensusFromDB converts a db.Census to an OrganizationCensus.
func OrganizationCensusFromDB(census *db.Census) OrganizationCensus {
	if census == nil {
		return OrganizationCensus{}
	}
	return OrganizationCensus{
		ID:          census.ID.Hex(),
		Type:        census.Type,
		OrgAddress:  census.OrgAddress,
		Size:        census.Size,
		Weighted:    census.Weighted,
		GroupID:     census.GroupID.Hex(),
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

// AddMembersRequest defines the payload for adding members to an organization.
// swagger:model AddMembersRequest
type AddMembersRequest struct {
	// List of members to add
	Members []OrgMember `json:"members"`
}

// ToDB converts the members in the request to db.OrgMember objects.
func (r *AddMembersRequest) ToDB() []*db.OrgMember {
	members := make([]*db.OrgMember, 0, len(r.Members))
	for _, p := range r.Members {
		members = append(members, p.ToDB())
	}
	return members
}

// AddCensusParticipantsRequest defines the payload for adding existing
// organization members to an existing census.
// swagger:model AddCensusParticipantsRequest
type AddCensusParticipantsRequest struct {
	// List of existing organization member IDs to add to the census
	MemberIDs []string `json:"memberIds"`
}

type DeleteMembersRequest struct {
	// List of member internal ids numbers to delete (optional if All is true)
	IDs []string `json:"ids,omitempty"`
	// Delete all members of the organization
	All bool `json:"all,omitempty"`
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
	ID string `json:"id"`

	// Unique member number as defined by the organization
	MemberNumber string `json:"memberNumber,omitempty"`

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

	// Member's census weight
	Weight string `json:"weight,omitempty"`

	// Additional custom fields
	Other map[string]any `json:"other,omitempty"`
}

// ToDB converts an OrgMember to a db.OrgMember.
func (p *OrgMember) ToDB() *db.OrgMember {
	// TODO: this could happen right during UnmarshalJSON,
	// if apicommon.OrgMember.ID is an ObjectID rather than a string.
	id := primitive.NilObjectID
	if len(p.ID) > 0 {
		// Convert the ID from string to ObjectID
		var err error
		id, err = primitive.ObjectIDFromHex(p.ID)
		if err != nil {
			log.Warnf("Failed to convert member ID %s to ObjectID: %v", p.ID, err)
		}
	}
	// if the weight is provided convert it to int, defaults to 1
	// we are performing the conversion here to avoid having a parsedweight field in the db
	weight := uint64(1)
	if p.Weight != "" {
		// convert only if non-empty string since ParseUint64 returns 0 if empty string
		var ok bool
		if weight, ok = math.ParseUint64(p.Weight); !ok {
			log.Warnf("Failed to convert member weight %s to int", p.Weight)
		}
	}

	return &db.OrgMember{
		ID:             id,
		MemberNumber:   p.MemberNumber,
		Name:           p.Name,
		Surname:        p.Surname,
		NationalID:     p.NationalID,
		BirthDate:      p.BirthDate,
		Email:          p.Email,
		PlaintextPhone: p.Phone,
		Password:       p.Password,
		Weight:         weight,
		Other:          p.Other,
	}
}

func OrgMemberFromDb(p db.OrgMember) OrgMember {
	return OrgMember{
		ID:           p.ID.Hex(),
		MemberNumber: p.MemberNumber,
		Name:         p.Name,
		Surname:      p.Surname,
		NationalID:   p.NationalID,
		BirthDate:    p.BirthDate,
		Email:        p.Email,
		Phone:        p.Phone.String(), // This returns either "" or the masked hash
		Other:        p.Other,
		Weight:       fmt.Sprintf("%d", p.Weight),
	}
}

type OrganizationMembersResponse struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
	// Total members in the organization
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
	// Organization address
	OrgAddress common.Address `json:"orgAddress"`

	// Vochain ID/Address of the process
	Address internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Census ID
	CensusID internal.HexBytes `json:"censusId" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Additional metadata for the process
	// Can be any key-value pairs
	Metadata map[string]any `json:"metadata"`

	// Optional high-level election parameters (used later at publish time)
	ElectionParams *db.ElectionParams `json:"electionParams,omitempty"`
}

// UpdateProcessRequest defines the payload for updating an existing voting process.
// swagger:model UpdateProcessRequest
type UpdateProcessRequest struct {
	// Vochain ID/Address of the process
	Address internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Census ID
	CensusID internal.HexBytes `json:"censusId" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Additional metadata for the process
	// Can be any key-value pairs
	Metadata map[string]any `json:"metadata"`

	// Optional high-level election parameters (used later at publish time)
	ElectionParams *db.ElectionParams `json:"electionParams,omitempty"`
}

// PublishProcessResponse is returned by POST /process/{processId}/publish with the
// on-chain process id and status after a draft has been published as an election.
// swagger:model PublishProcessResponse
type PublishProcessResponse struct {
	// On-chain process id (Vochain election id)
	Address internal.HexBytes `json:"address" swaggertype:"string" format:"hex" example:"deadbeef"`

	// Process status (e.g. "READY")
	Status string `json:"status"`
}

// ProcessResultsResponse is the trimmed on-chain election state returned by
// GET /process/{processId}/results.
type ProcessResultsResponse struct {
	Status       string     `json:"status"`
	VoteCount    uint64     `json:"voteCount"`
	StartDate    time.Time  `json:"startDate"`
	EndDate      time.Time  `json:"endDate"`
	FinalResults bool       `json:"finalResults"`
	Results      [][]string `json:"results,omitempty"`
}

// RelayVoteRequest is the body of POST /vote: a hex-encoded, already-signed voter
// transaction (a marshaled models.SignedTx wrapping a Vote tx). The target process
// is taken from the inner Vote envelope.
// swagger:model RelayVoteRequest
type RelayVoteRequest struct {
	// Hex of a marshaled models.SignedTx whose inner Tx is a Vote
	TxPayload internal.HexBytes `json:"txPayload" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// RelayVoteResponse is returned by POST /vote with the vote nullifier (voteID)
// assigned on chain.
// swagger:model RelayVoteResponse
type RelayVoteResponse struct {
	// On-chain vote nullifier
	VoteID internal.HexBytes `json:"voteID" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// SetProcessStatusRequest is the body of PUT /process/{processId}/status.
// swagger:model SetProcessStatusRequest
type SetProcessStatusRequest struct {
	// One of: ready, paused, ended, canceled
	Status string `json:"status" example:"paused"`
}

// SetProcessStatusResponse is returned by PUT /process/{processId}/status with the
// new (cached) status.
// swagger:model SetProcessStatusResponse
type SetProcessStatusResponse struct {
	// Process status (e.g. "PAUSED")
	Status string `json:"status"`
}

// EnqueuedResponse is returned with 202 Accepted by the async transaction endpoints
// (publish, status, vote). The client polls GET /jobs/{jobId} to obtain the result.
// swagger:model EnqueuedResponse
type EnqueuedResponse struct {
	// Opaque job id; poll GET /jobs/{jobId} for the outcome
	JobID string `json:"jobId" example:"a1b2c3"`
}

// JobStatusResponse is returned by GET /jobs/{jobId}. It always responds 200; the
// Status field carries the lifecycle state (pending|completed|failed). Result is set
// only when completed; Error only when failed.
// swagger:model JobStatusResponse
type JobStatusResponse struct {
	JobID  string        `json:"jobId"`
	Type   db.JobType    `json:"type"`
	Status db.JobStatus  `json:"status"`
	Result *db.JobResult `json:"result,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// InitiateAuthRequest defines the payload for participant authentication.
// swagger:model InitiateAuthRequest
type InitiateAuthRequest struct {
	// Unique participant ID
	ParticipantID string `json:"participantId"`

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
	CensusID string `json:"censusId"`

	// List of processes to include in the bundle. Each entry is either the 24-hex ProcessID or the
	// 64-hex on-chain election id.
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

// CheckBundleParticipantsRequest is the payload for the bundle participant
// membership check endpoint. fieldName must be one of: "email", "phone",
// "memberNumber", "nationalId". value is the raw value to look up — for
// "phone" the caller passes the plaintext number and the backend hashes it
// server-side before the lookup. processID is the hex ID of the process whose
// voting status is reported in the response (hasVoted is true when the member
// has consumed that process).
// swagger:model CheckBundleParticipantsRequest
type CheckBundleParticipantsRequest struct {
	FieldName string            `json:"fieldName"`
	Value     string            `json:"value"`
	ProcessID internal.HexBytes `json:"processID" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// CheckBundleParticipantsResponseEntry describes a single org member that
// matched the lookup and is a participant of the bundle's census. HasVoted is
// true when the member has a used CSP process for the request's processID
// (i.e. has consumed the process to cast a ballot).
// swagger:model CheckBundleParticipantsResponseEntry
type CheckBundleParticipantsResponseEntry struct {
	MemberID     string `json:"memberId"`
	Name         string `json:"name,omitempty"`
	Surname      string `json:"surname,omitempty"`
	Email        string `json:"email,omitempty"`
	MemberNumber string `json:"memberNumber,omitempty"`
	HasVoted     bool   `json:"hasVoted"`
}

// CheckBundleParticipantsResponse is the response for the bundle participant
// membership check endpoint. The participants slice contains only members that
// match the lookup AND are participants of the bundle's census. An empty
// slice means no match.
// swagger:model CheckBundleParticipantsResponse
type CheckBundleParticipantsResponse struct {
	Participants []CheckBundleParticipantsResponseEntry `json:"participants"`
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
	// OAuth provider name (google, github, facebook)
	Provider string `json:"provider"`
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

// OAuthLinkRequest defines the payload for linking an OAuth provider to an existing account.
// swagger:model OAuthLinkRequest
type OAuthLinkRequest struct {
	// OAuth provider name (google, github, facebook)
	Provider string `json:"provider"`
	// The signature made by the OAuth service on top of the user email
	OAuthSignature string `json:"oauthSignature"`
	// The signature made by the user on top of the oauth signature
	UserOAuthSignature string `json:"userOAuthSignature"`
	// The address of the user
	Address string `json:"address"`
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

// JobInfo represents a job in the API response.
// swagger:model JobInfo
type JobInfo struct {
	// Unique job identifier
	JobID string `json:"jobId"`

	// Type of job
	Type db.JobType `json:"type"`

	// Total items to process
	Total int `json:"total"`

	// Items successfully processed
	Added int `json:"added"`

	// List of errors encountered
	Errors []string `json:"errors"`

	// Job creation timestamp
	CreatedAt time.Time `json:"createdAt"`

	// Job completion timestamp (zero if not completed)
	CompletedAt time.Time `json:"completedAt"`

	// Whether the job is completed
	Completed bool `json:"completed"`
}

// JobsResponse represents the response for listing organization jobs.
// swagger:model JobsResponse
type JobsResponse struct {
	// Pagination fields
	Pagination *Pagination `json:"pagination"`
	// List of jobs
	Jobs []JobInfo `json:"jobs"`
}

// JobFromDB converts a db.Job to a JobInfo.
func JobFromDB(job *db.Job) JobInfo {
	if job == nil {
		return JobInfo{}
	}
	return JobInfo{
		JobID:       job.JobID,
		Type:        job.Type,
		Total:       job.Total,
		Added:       job.Added,
		Errors:      job.Errors,
		CreatedAt:   job.CreatedAt,
		CompletedAt: job.CompletedAt,
		Completed:   !job.CompletedAt.IsZero(),
	}
}
