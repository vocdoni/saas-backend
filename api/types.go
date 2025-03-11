package api

import (
	"time"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
)

// Organization is the struct that represents an organization in the API
type OrganizationInfo struct {
	Address        string               `json:"address"`
	Website        string               `json:"website"`
	CreatedAt      string               `json:"createdAt"`
	Type           string               `json:"type"`
	Size           string               `json:"size"`
	Color          string               `json:"color"`
	Subdomain      string               `json:"subdomain"`
	Country        string               `json:"country"`
	Timezone       string               `json:"timezone"`
	Active         bool                 `json:"active"`
	Communications bool                 `json:"communications"`
	Subscription   *SubscriptionDetails `json:"subscription"`
	Counters       *SubscriptionUsage   `json:"counters"`
	Parent         *OrganizationInfo    `json:"parent"`
}

// OrganizationMembers is the struct that represents a list of members of
// organizations in the API.
type OrganizationMembers struct {
	Members []*OrganizationMember `json:"members"`
}

// OrganizationMember is the struct that represents a members of organizations
// with his role in the API.
type OrganizationMember struct {
	Info *UserInfo `json:"info"`
	Role string    `json:"role"`
}

// OrganizationAddresses is the struct that represents a list of addresses of
// organizations in the API.
type OrganizationAddresses struct {
	Addresses []string `json:"addresses"`
}

// UserOrganization is the struct that represents the organization of a user in
// the API, including the role of the user in the organization.
type UserOrganization struct {
	Role         string            `json:"role"`
	Organization *OrganizationInfo `json:"organization"`
}

// OrganizationRole is the struct that represents the role of an organization
// member in the API.
type OrganizationRole struct {
	Role            string `json:"role"`
	Name            string `json:"name"`
	WritePermission bool   `json:"writePermission"`
}

// OrganizationRoleList is the struct that represents a list of roles of an
// organization member in the API.
type OrganizationRoleList struct {
	Roles []*OrganizationRole `json:"roles"`
}

// OrganizationType is the struct that represents the type of an organization in
// the API.
type OrganizationType struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// OrganizationTypeList is the struct that represents a list of types of
// organizations in the API.
type OrganizationTypeList struct {
	Types []*OrganizationType `json:"types"`
}

// UserInfo is the request to register a new user.
type UserInfo struct {
	Email         string              `json:"email,omitempty"`
	Password      string              `json:"password,omitempty"`
	FirstName     string              `json:"firstName,omitempty"`
	LastName      string              `json:"lastName,omitempty"`
	Verified      bool                `json:"verified,omitempty"`
	Organizations []*UserOrganization `json:"organizations"`
}

// OrganizationInvite is the struct that represents an invitation to an
// organization in the API.
type OrganizationInvite struct {
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	Expiration time.Time `json:"expiration"`
}

// OrganizationInviteList is the struct that represents a list of invitations to
// organizations in the API.
type OrganizationInviteList struct {
	Invites []*OrganizationInvite `json:"pending"`
}

// AcceptOrganizationInvitation is the request to accept an invitation to an
// organization.
type AcceptOrganizationInvitation struct {
	Code string    `json:"code"`
	User *UserInfo `json:"user"`
}

// UserPasswordUpdate is the request to update the password of a user.
type UserPasswordUpdate struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// UserVerificationRequest is the request to verify a user.
type UserVerification struct {
	Email      string    `json:"email,omitempty"`
	Code       string    `json:"code,omitempty"`
	Expiration time.Time `json:"expiration,omitempty"`
	Valid      bool      `json:"valid"`
}

// UserPasswordReset is the request to reset the password of a user.
type UserPasswordReset struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"newPassword"`
}

// LoginResponse is the response of the login request which includes the JWT token
type LoginResponse struct {
	Token    string    `json:"token"`
	Expirity time.Time `json:"expirity"`
}

// TransactionData is the struct that contains the data of a transaction to
// be signed, but also is used to return the signed transaction.
type TransactionData struct {
	Address   internal.HexBytes `json:"address"`
	TxPayload []byte            `json:"txPayload"`
}

// MessageSignature is the struct that contains the payload and the signature.
// Its used to receive and return a signed message.
type MessageSignature struct {
	Address   string            `json:"address"`
	Payload   []byte            `json:"payload,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
}

// organizationFromDB converts a db.Organization to an OrganizationInfo, if the parent
// organization is provided it will be included in the response.
func organizationFromDB(dbOrg, parent *db.Organization) *OrganizationInfo {
	if dbOrg == nil {
		return nil
	}
	var parentOrg *OrganizationInfo
	if parent != nil {
		parentOrg = organizationFromDB(parent, nil)
	}
	details := subscriptionDetailsFromDB(&dbOrg.Subscription)
	usage := subscriptionUsageFromDB(&dbOrg.Counters)
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

// OrganizationSubscriptionInfo is the struct used to provide detailed information
// regaridng the subscription of an organization.
type OrganizationSubscriptionInfo struct {
	SubcriptionDetails SubscriptionDetails `json:"subscriptionDetails"`
	Usage              SubscriptionUsage   `json:"usage"`
	Plan               SubscriptionPlan    `json:"plan"`
}

// SubscriptionPlan is the struct that represents a subscription plan in the
// API. It is the mirror struct of db.Plan.
type SubscriptionPlan struct {
	ID              uint64                  `json:"id"`
	Name            string                  `json:"name"`
	StripeID        string                  `json:"stripeID"`
	StripePriceID   string                  `json:"stripePriceID"`
	StartingPrice   int64                   `json:"startingPrice"`
	Default         bool                    `json:"default"`
	Organization    SubscriptionPlanLimits  `json:"organization"`
	VotingTypes     SubscriptionVotingTypes `json:"votingTypes"`
	Features        SubscriptionFeatures    `json:"features"`
	CensusSizeTiers []SubscriptionPlanTier  `json:"censusSizeTiers"`
}

func subscriptionPlanFromDB(plan *db.Plan) SubscriptionPlan {
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

// SubscriptionPlanLimits represents the limits of a subscription plan. It is
// the mirror struct of db.PlanLimits.
type SubscriptionPlanLimits struct {
	Members      int  `json:"members"`
	SubOrgs      int  `json:"subOrgs"`
	MaxProcesses int  `json:"maxProcesses"`
	MaxCensus    int  `json:"maxCensus"`
	MaxDuration  int  `json:"maxDuration"`
	CustomURL    bool `json:"customURL"`
	Drafts       int  `json:"drafts"`
}

// SubscriptionVotingTypes represents the voting types of a subscription plan.
// It is the mirror struct of db.VotingTypes.
type SubscriptionVotingTypes struct {
	Single     bool `json:"single"`
	Multiple   bool `json:"multiple"`
	Approval   bool `json:"approval"`
	Cumulative bool `json:"cumulative"`
	Ranked     bool `json:"ranked"`
	Weighted   bool `json:"weighted"`
}

// SubscriptionFeatures represents the features of a subscription plan. It is
// the mirror struct of db.Features.
type SubscriptionFeatures struct {
	Anonymous       bool `json:"anonymous"`
	Overwrite       bool `json:"overwrite"`
	LiveResults     bool `json:"liveResults"`
	Personalization bool `json:"personalization"`
	EmailReminder   bool `json:"emailReminder"`
	SmsNotification bool `json:"smsNotification"`
	WhiteLabel      bool `json:"whiteLabel"`
	LiveStreaming   bool `json:"liveStreaming"`
}

// SubscriptionPlanTier represents a tier of a subscription plan. It is the
// mirror struct of db.PlanTier.
type SubscriptionPlanTier struct {
	Amount int64 `json:"amount"`
	UpTo   int64 `json:"upTo"`
}

// SubscriptionDetails represents the details of an organization subscription.
// It is the mirror struct of db.OrganizationSubscription.
type SubscriptionDetails struct {
	PlanID          uint64    `json:"planID"`
	StartDate       time.Time `json:"startDate"`
	RenewalDate     time.Time `json:"renewalDate"`
	LastPaymentDate time.Time `json:"lastPaymentDate"`
	Active          bool      `json:"active"`
	MaxCensusSize   int       `json:"maxCensusSize"`
	Email           string    `json:"email"`
}

func subscriptionDetailsFromDB(details *db.OrganizationSubscription) SubscriptionDetails {
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

// SubscriptionUsage represents the usage of an organization subscription. It is
// the mirror struct of db.OrganizationCounters.
type SubscriptionUsage struct {
	SentSMS    int `json:"sentSMS"`
	SentEmails int `json:"sentEmails"`
	SubOrgs    int `json:"subOrgs"`
	Members    int `json:"members"`
	Processes  int `json:"processes"`
}

func subscriptionUsageFromDB(usage *db.OrganizationCounters) SubscriptionUsage {
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
type SubscriptionCheckout struct {
	LookupKey uint64 `json:"lookupKey"`
	ReturnURL string `json:"returnURL"`
	Amount    int64  `json:"amount"`
	Address   string `json:"address"`
	Locale    string `json:"locale"`
}

// ParticipantNotification is the struct that represents a notification for a
// participant in the API.
type ParticipantNotification struct {
	ProcessID    []byte                     `json:"processID"`
	Notification notifications.Notification `json:"notification"`
	Sent         bool                       `json:"sent"`
	SentAt       time.Time                  `json:"sentAt"`
}

// OrganizationCensus is the struct that represents a census of an organization
// in the API. It is the mirror struct of db.Census.
type OrganizationCensus struct {
	ID         string        `json:"censusID"`
	Type       db.CensusType `json:"type"`
	OrgAddress string        `json:"orgAddress"`
}

func organizationCensusFromDB(census *db.Census) OrganizationCensus {
	if census == nil {
		return OrganizationCensus{}
	}
	return OrganizationCensus{
		ID:         census.ID.Hex(),
		Type:       census.Type,
		OrgAddress: census.OrgAddress,
	}
}

// OrganizationCensuses is the struct to wrap a list of censuses of an
// organization in the API.
type OrganizationCensuses struct {
	Censuses []OrganizationCensus `json:"censuses"`
}

// AddParticipantsRequest defines the payload for adding participants to a census
type AddParticipantsRequest struct {
	Participants []OrgParticipant `json:"participants"`
}

func (r *AddParticipantsRequest) dbOrgParticipants(orgAddress string) []db.OrgParticipant {
	participants := make([]db.OrgParticipant, 0, len(r.Participants))
	for _, p := range r.Participants {
		participants = append(participants, p.toDB(orgAddress))
	}
	return participants
}

// OrgParticipant defines the structure of a participant in the API. It is the
// mirror struct of db.OrgParticipant.
type OrgParticipant struct {
	ParticipantNo string         `json:"participantNo"`
	Name          string         `json:"name"`
	Email         string         `json:"email"`
	Phone         string         `json:"phone"`
	Password      string         `json:"password"`
	Other         map[string]any `json:"other"`
}

func (p *OrgParticipant) toDB(orgAddress string) db.OrgParticipant {
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

// AddParticipantsResponse defines the response for successful participant addition
type AddParticipantsResponse struct {
	ParticipantsNo uint32            `json:"participantsNo"`
	JobID          internal.HexBytes `json:"jobID"`
}

// Request types for process operations

// CreateProcessRequest defines the payload for creating a new voting process
type CreateProcessRequest struct {
	PublishedCensusRoot string `json:"censusRoot"`
	PublishedCensusURI  string `json:"censusUri"`
	CensusID            string `json:"censusID"`
	Metadata            []byte `json:"metadata,omitempty"`
}

// InitiateAuthRequest defines the payload for participant authentication
type InitiateAuthRequest struct {
	ParticipantNo string `json:"participantNo"`
	Email         string `json:"email,omitempty"`
	Phone         string `json:"phone,omitempty"`
	Password      string `json:"password,omitempty"`
}

// VerifyAuthRequest defines the payload for auth code verification
type VerifyAuthRequest struct {
	Token string `json:"token"`
	Code  string `json:"code"`
}

// GenerateProofRequest defines the payload for generating voting proof
type GenerateProofRequest struct {
	Token          string `json:"token"`
	BlindedAddress []byte `json:"blindedAddress"`
}
