package apicommon

//revive:disable:max-public-structs

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// multilingualFromAny extracts a MultilingualText from a meta map value. It handles:
//   - plain string (legacy storage): normalised to {"default": "<string>"}
//   - MultilingualText / map[string]string (in-memory, set at creation time)
//   - map[string]any (BSON-decoded form; plain, not bson.M — see db/mongo.go)
func multilingualFromAny(v any) *MultilingualText {
	switch m := v.(type) {
	case string:
		r := MultilingualText{"default": m}
		return &r
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

// OrgDisplayName returns the "default" value of meta["name"] as a plain string, falling
// back to fallback (typically the org's hex address) when the field is absent or empty.
func OrgDisplayName(meta map[string]any, fallback string) string {
	if mt := multilingualFromAny(meta["name"]); mt != nil {
		if def := (*mt)["default"]; def != "" {
			return def
		}
	}
	return fallback
}

// BuildOrgMeta merges the convenience name/logo/description fields with an explicit meta
// map on top of base (nil starts from an empty map). Precedence, lowest to highest:
// base keys → shorthand fields → explicit meta keys.
func BuildOrgMeta(base map[string]any, name, logo, description *MultilingualText, explicit map[string]any) map[string]any {
	meta := make(map[string]any, len(base))
	maps.Copy(meta, base)
	// stored as unnamed map[string]string so db.Organization accessors
	// (DisplayName/LogoURL) match the value before any Mongo round-trip
	if name != nil {
		meta["name"] = map[string]string(*name)
	}
	if logo != nil {
		meta["logo"] = map[string]string(*logo)
	}
	if description != nil {
		meta["description"] = map[string]string(*description)
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

	// Language of the notifications sent on behalf of the organization. It wins on
	// authenticated endpoints; on public ones an explicit lang param wins instead.
	// Optional on creation, where it defaults to en; empty on update means unchanged
	DefaultLang string `json:"defaultLang"`

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

// OrganizationLanguageList represents the languages supported for notifications.
// swagger:model OrganizationLanguageList
type OrganizationLanguageList struct {
	// Languages accepted for notifications, both as the lang query parameter
	// and as an organization defaultLang
	Languages []string `json:"languages"`
	// Default language used when no other applies
	Default string `json:"default"`
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
	// copy dbOrg.Meta into a fresh map: we normalize legacy string values below
	// and must not mutate the db model in-place, since callers don't expect this
	// read/convert helper to have side effects. A nil Meta is normalized to an
	// empty map so responses are consistent: the DB read path already does this,
	// but dbOrg may be built in-memory (e.g. at creation) where Meta is nil,
	// which would otherwise emit "meta": null.
	meta := make(map[string]any, len(dbOrg.Meta))
	maps.Copy(meta, dbOrg.Meta)
	// Upgrade any plain-string values for the well-known keys to the object
	// form so that meta.name and the top-level name field always agree.
	for _, key := range []string{"name", "logo", "description"} {
		if s, ok := meta[key].(string); ok {
			meta[key] = MultilingualText{"default": s}
		}
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
		DefaultLang:    dbOrg.DefaultLang,
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

// CreateManagedOrganizationRequest is the body of POST /integrator/organizations.
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
// /integrator/organizations/{orgAddress} confirming the deleted address.
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

// IntegratorInfoResponse is returned by the path-less GET /integrator.
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
	// Whether zk-SNARK anonymous voting is available. Blind-CSP anonymous voting (census.anonymous) is available on every plan.
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

// OrganizationCensusFromDB converts a db.Census to an OrganizationCensus.
func OrganizationCensusFromDB(census *db.Census) OrganizationCensus {
	if census == nil {
		return OrganizationCensus{}
	}
	out := OrganizationCensus{
		ID:          census.ID.Hex(),
		Type:        census.Type,
		OrgAddress:  census.OrgAddress,
		Size:        census.Size,
		Weighted:    census.Weighted,
		AuthFields:  census.AuthFields,
		TwoFaFields: census.TwoFaFields,
	}
	// guard the zero id so an organization-wide census reports no group at all: omitempty keys off the
	// empty string, but a zero ObjectID hexes to 24 zeros and would serialize as a real group.
	if !census.GroupID.IsZero() {
		out.GroupID = census.GroupID.Hex()
	}
	return out
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

	// CensusJobIDs are the async jobs raising the on-chain maxCensusSize of the elections whose
	// questions the deletion opened to the whole census (pruning an eligibility list to empty is
	// "no restriction", not "nobody"). Poll each with GET /jobs/{jobId}. Deleting a member has the
	// same on-chain effect through this endpoint as through DELETE /processes/{processId}/census,
	// which reports its job — so this reports it too.
	CensusJobIDs []string `json:"censusJobIds,omitempty"`

	// Errors are resize problems that did not stop the deletion. The members are gone either
	// way; what may be missing is the on-chain room for a question the deletion opened to the
	// whole census. An empty CensusJobIDs alone cannot report that — a deletion that needed no
	// resize looks identical.
	Errors []string `json:"errors,omitempty"`
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
	id := bson.NilObjectID
	if len(p.ID) > 0 {
		// Convert the ID from string to ObjectID
		var err error
		id, err = bson.ObjectIDFromHex(p.ID)
		if err != nil {
			log.Warnf("failed to convert member ID %s to ObjectID: %v", p.ID, err)
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

// UpsertOrgMemberRequest creates or updates an organization member. On an update a field left
// out of the request keeps its stored value, while a field sent empty is cleared. Phone and
// password are never returned in plaintext, so leave them out to keep them.
// swagger:model UpsertOrgMemberRequest
type UpsertOrgMemberRequest struct {
	// Member's internal unique ID. Empty, or one naming no member of the organization, creates one.
	ID string `json:"id"`

	// Unique member number as defined by the organization
	MemberNumber *string `json:"memberNumber,omitempty"`

	// Member's name
	Name *string `json:"name,omitempty"`

	// Member's surname
	Surname *string `json:"surname,omitempty"`

	// Member's National ID No
	NationalID *string `json:"nationalId,omitempty"`

	// Member's date of birth in format YYYY-MM-DD
	BirthDate *string `json:"birthDate,omitempty"`

	// Member's email address
	Email *string `json:"email,omitempty"`

	// Member's phone number
	Phone *string `json:"phone,omitempty"`

	// Member's password (for authentication)
	Password *string `json:"password,omitempty"`

	// Member's census weight. Empty sets the default 1, as does leaving it out of a new member.
	Weight *string `json:"weight,omitempty"`

	// Additional custom fields, replaced as a whole
	Other map[string]any `json:"other,omitempty"`
}

// ToDB converts the request into a db.OrgMemberUpdate.
func (r *UpsertOrgMemberRequest) ToDB() (*db.OrgMemberUpdate, error) {
	update := &db.OrgMemberUpdate{
		MemberNumber: r.MemberNumber,
		Name:         r.Name,
		Surname:      r.Surname,
		NationalID:   r.NationalID,
		BirthDate:    r.BirthDate,
		Email:        r.Email,
		Phone:        r.Phone,
		Password:     r.Password,
		Other:        r.Other,
	}
	if r.ID != "" {
		id, err := bson.ObjectIDFromHex(r.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid member id %q: %w", r.ID, err)
		}
		update.ID = id
	}
	if r.Weight != nil {
		// a cleared weight is the default, not 0: ParseUint64 reads "" as 0
		weight := uint64(1)
		if *r.Weight != "" {
			var ok bool
			if weight, ok = math.ParseUint64(*r.Weight); !ok {
				return nil, fmt.Errorf("invalid weight %q", *r.Weight)
			}
		}
		update.Weight = &weight
	}
	return update, nil
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

	// Errors encountered during job. Validation errors are prefixed with
	// "line N:", the 1-based position of the offending member in the
	// submitted members list.
	Errors []string `json:"errors"`

	// Job ID for tracking the addition process
	JobID internal.HexBytes `json:"jobId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`

	// CensusJobIDs are the async jobs raising the on-chain maxCensusSize of the elections whose
	// census just grew, one per organization. Poll each with GET /jobs/{jobId}. Absent when no
	// live census was affected.
	CensusJobIDs []string `json:"censusJobIds,omitempty"`
}

// UpsertOrgMemberResponse is returned by PUT /organizations/{orgAddress}/members. The id is the
// member's; censusJobIds are present only when creating the member grew a live census and the
// on-chain maxCensusSize had to be raised.
// swagger:model UpsertOrgMemberResponse
type UpsertOrgMemberResponse struct {
	// Member's internal unique ID
	ID string `json:"id"`

	// CensusJobIDs are the async jobs raising the on-chain maxCensusSize of the affected
	// elections. Poll each with GET /jobs/{jobId}.
	CensusJobIDs []string `json:"censusJobIds,omitempty"`

	// Errors are per-census problems that did not stop the member from being written. The member
	// exists either way; what may be missing is their place in a live census, or the on-chain room
	// for them to vote. Empty CensusJobIDs alone cannot report that — a create that needed no
	// resize looks identical.
	Errors []string `json:"errors,omitempty"`
}

// UpdateOrganizationMemberGroupResponse is returned by PUT
// /organizations/{orgAddress}/groups/{groupId} when there is something to report. The endpoint
// answered a bare OK before and still does when both lists are empty, so existing clients are
// unaffected.
// swagger:model UpdateOrganizationMemberGroupResponse
type UpdateOrganizationMemberGroupResponse struct {
	// CensusJobIDs are the async jobs raising the on-chain maxCensusSize of the elections whose
	// census just grew or reopened. Poll each with GET /jobs/{jobId}.
	CensusJobIDs []string `json:"censusJobIds,omitempty"`

	// Errors are per-census problems that did not stop the group update.
	Errors []string `json:"errors,omitempty"`
}

// Request types for process operations

// RelayVoteRequest is the body of POST /vote: a hex-encoded, already-signed voter
// transaction (a marshaled models.SignedTx wrapping a Vote tx). The target process
// is taken from the inner Vote envelope.
// swagger:model RelayVoteRequest
type RelayVoteRequest struct {
	// Hex of a marshaled models.SignedTx whose inner Tx is a Vote
	TxPayload internal.HexBytes `json:"txPayload" swaggertype:"string" format:"hex" example:"deadbeef"`
}

// RelayVotesRequest is the body of POST /votes: the already-signed voter transactions of
// one voter, typically the questions of a multi-question voting process. The batch is
// accepted or rejected as a unit and relayed under a single job, whose result reports the
// votes in the order they are given here.
// swagger:model RelayVotesRequest
type RelayVotesRequest struct {
	// Signed vote transactions, at most 100
	Votes []RelayVoteRequest `json:"votes"`
}

// VerifyVotesRequest is the body of POST /votes/verify: the vote nullifiers a voter wants
// checked against the chain, typically the one nullifier per question of a multi-question
// voting process.
// swagger:model VerifyVotesRequest
type VerifyVotesRequest struct {
	// Vote nullifiers (up to 32 bytes each — anonymous ones may be shorter), capped at the
	// shared vote batch limit (100)
	Nullifiers []internal.HexBytes `json:"nullifiers" swaggertype:"array,string"`
}

// VerifiedVote is the outcome of checking one nullifier against the chain. Everything
// beyond Verified comes from the on-chain vote and is absent when it was not found.
// swagger:model VerifiedVote
type VerifiedVote struct {
	// The nullifier that was checked, echoed back
	Nullifier internal.HexBytes `json:"nullifier" swaggertype:"string" format:"hex" example:"deadbeef"`
	// Whether the chain has a vote with this nullifier
	Verified bool `json:"verified"`
	// On-chain election id the vote belongs to
	ProcessID internal.HexBytes `json:"processId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	// Hash of the transaction that carried the vote
	TxHash internal.HexBytes `json:"txHash,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	// Block the vote was registered in
	BlockHeight uint32 `json:"blockHeight,omitempty"`
	// When the vote was registered on chain
	Date *time.Time `json:"date,omitempty"`
}

// VerifyVotesResponse is returned by POST /votes/verify, one entry per requested
// nullifier in the order they were given.
// swagger:model VerifyVotesResponse
type VerifyVotesResponse struct {
	Votes []VerifiedVote `json:"votes"`
}

// SetProcessStatusRequest is the body of PUT /processes/{processId}/questions/{questionId}/status.
// swagger:model SetProcessStatusRequest
type SetProcessStatusRequest struct {
	// One of: READY, PAUSED, ENDED, CANCELED (case-insensitive on input; stored/returned uppercase)
	Status string `json:"status" example:"PAUSED"`
}

// EnqueuedResponse is returned with 202 Accepted by the async transaction endpoints
// (publish, status, vote). The client polls GET /jobs/{jobId} to obtain the result.
// swagger:model EnqueuedResponse
type EnqueuedResponse struct {
	// Opaque job id; poll GET /jobs/{jobId} for the outcome
	JobID string `json:"jobId" example:"a1b2c3"`
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

// UnifiedJobResult is the merged result payload of GET /jobs: a superset of the tx-job outcome
// (address/status/processId/nullifier/voteID, or votes for a batch vote relay) and the import-job
// counters (added/progress/total). Empty attributes are omitted so a job only surfaces what its
// type produced.
type UnifiedJobResult struct {
	Address   internal.HexBytes `json:"address,omitempty" swaggertype:"string" example:"deadbeef"`
	Status    string            `json:"status,omitempty"`
	ProcessID internal.HexBytes `json:"processId,omitempty" swaggertype:"string" example:"deadbeef"`
	Nullifier internal.HexBytes `json:"nullifier,omitempty" swaggertype:"string" example:"deadbeef"`
	VoteID    internal.HexBytes `json:"voteID,omitempty" swaggertype:"string" example:"deadbeef"`
	// Votes is the per-envelope outcome of a batch vote relay, in request order
	Votes    []db.VoteJobResult `json:"votes,omitempty"`
	Added    int                `json:"added,omitempty"`
	Progress int                `json:"progress,omitempty"`
	Total    int                `json:"total,omitempty"`
}

func (r *UnifiedJobResult) isEmpty() bool {
	return len(r.Address) == 0 && r.Status == "" && len(r.ProcessID) == 0 && len(r.Nullifier) == 0 &&
		len(r.VoteID) == 0 && len(r.Votes) == 0 && r.Added == 0 && r.Progress == 0 && r.Total == 0
}

// JobResponse is one job in the GET /jobs list: the unified shape across import and tx jobs.
// swagger:model JobResponse
type JobResponse struct {
	JobID  string            `json:"jobId"`
	Type   db.JobType        `json:"type"`
	Status db.JobStatus      `json:"status,omitempty"`
	Errors []string          `json:"errors,omitempty"`
	Result *UnifiedJobResult `json:"result,omitempty"`
}

// JobsListResponse is the paginated response of GET /jobs.
// swagger:model JobsListResponse
type JobsListResponse struct {
	Pagination *Pagination   `json:"pagination"`
	Jobs       []JobResponse `json:"jobs"`
}

// JobResponseFromDB builds the unified job response from a db.Job. Import jobs (which don't set
// Status) fall back to completed/pending derived from CompletedAt, and expose added/progress/total;
// tx jobs expose their Result (address/status/processId/nullifier/voteID, or the per-vote entries of
// a batch relay). The result is omitted when empty.
func JobResponseFromDB(job *db.Job) JobResponse {
	status := job.Status
	if status == "" {
		status = db.JobStatusPending
		if !job.CompletedAt.IsZero() {
			status = db.JobStatusCompleted
		}
	}
	errs := job.Errors
	if len(errs) == 0 && job.Error != "" {
		errs = []string{job.Error}
	}
	// A batch vote job whose envelopes have all reported is finished, whatever its stored status
	// says. The worker increments the counter and writes the terminal status as two operations, so
	// a read can land between them — and if the process dies in that gap the job would otherwise
	// stay pending forever, even though the outcome is fully determined by the entries. Derive it
	// with the same function the worker closes the job with, so the two can never disagree.
	if job.Type == db.JobTypeRelayVotes && status == db.JobStatusPending &&
		job.Total > 0 && job.Added >= job.Total && job.Result != nil {
		status, errs = db.TerminalVoteBatchStatus(job.Result.Votes)
	}
	res := &UnifiedJobResult{Added: job.Added, Total: job.Total}
	if job.Result != nil {
		res.Address = job.Result.Address
		res.Status = job.Result.Status
		res.ProcessID = job.Result.ProcessID
		res.Nullifier = job.Result.Nullifier
		res.VoteID = job.Result.VoteID
		res.Votes = job.Result.Votes
	}
	if job.Total > 0 {
		res.Progress = job.Added * 100 / job.Total
	}
	resp := JobResponse{JobID: job.JobID, Type: job.Type, Status: status, Errors: errs}
	if !res.isEmpty() {
		resp.Result = res
	}
	return resp
}
