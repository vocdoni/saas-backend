package api

import (
	"time"

	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/types"
)

// Organization is the struct that represents an organization in the API
type OrganizationInfo struct {
	Address   string            `json:"address"`
	Website   string            `json:"website"`
	CreatedAt string            `json:"createdAt"`
	Type      string            `json:"type"`
	Size      string            `json:"size"`
	Color     string            `json:"color"`
	Subdomain string            `json:"subdomain"`
	Country   string            `json:"country"`
	Timezone  string            `json:"timezone"`
	Active    bool              `json:"active"`
	Parent    *OrganizationInfo `json:"parent"`
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

// UserInfo is the request to register a new user.
type UserInfo struct {
	Email         string              `json:"email,omitempty"`
	Phone         string              `json:"phone,omitempty"`
	Password      string              `json:"password,omitempty"`
	FirstName     string              `json:"firstName,omitempty"`
	LastName      string              `json:"lastName,omitempty"`
	Verified      bool                `json:"verified,omitempty"`
	Organizations []*UserOrganization `json:"organizations"`
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
	Phone      string    `json:"phone,omitempty"`
	Expiration time.Time `json:"expiration,omitempty"`
	Valid      bool      `json:"valid"`
}

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
	Address   string `json:"address"`
	TxPayload string `json:"txPayload"`
}

// MessageSignature is the struct that contains the payload and the signature.
// Its used to receive and return a signed message.
type MessageSignature struct {
	Address   string         `json:"address"`
	Payload   []byte         `json:"payload,omitempty"`
	Signature types.HexBytes `json:"signature,omitempty"`
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
	return &OrganizationInfo{
		Address:   dbOrg.Address,
		Website:   dbOrg.Website,
		CreatedAt: dbOrg.CreatedAt.Format(time.RFC3339),
		Type:      string(dbOrg.Type),
		Size:      dbOrg.Size,
		Color:     dbOrg.Color,
		Subdomain: dbOrg.Subdomain,
		Country:   dbOrg.Country,
		Timezone:  dbOrg.Timezone,
		Active:    dbOrg.Active,
		Parent:    parentOrg,
	}
}
