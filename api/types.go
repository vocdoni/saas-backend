package api

import (
	"time"

	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/types"
)

// Organization is the struct that represents an organization in the API
type OrganizationInfo struct {
	Address     string            `json:"address"`
	Name        string            `json:"name"`
	CreatedAt   string            `json:"createdAt"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Size        uint64            `json:"size"`
	Color       string            `json:"color"`
	Logo        string            `json:"logo"`
	Subdomain   string            `json:"subdomain"`
	Timezone    string            `json:"timezone"`
	Active      bool              `json:"active"`
	Parent      *OrganizationInfo `json:"parent"`
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
	Password      string              `json:"password,omitempty"`
	FullName      string              `json:"fullName,omitempty"`
	Organizations []*UserOrganization `json:"organizations"`
}

// LoginResponse is the response of the login request which includes the JWT token
type LoginResponse struct {
	Token    string    `json:"token"`
	Expirity time.Time `json:"expirity"`
}

// TransactionData is the struct that contains the data of a transaction to
// be signed, but also is used to return the signed transaction.
type TransactionData struct {
	OrganizationAddress string `json:"organizationAddress"`
	TxPayload           string `json:"txPayload"`
}

// MessageSignature is the struct that contains the payload and the signature.
// Its used to receive and return a signed message.
type MessageSignature struct {
	OrganizationAddress string         `json:"organizationAddress"`
	Payload             []byte         `json:"payload,omitempty"`
	Signature           types.HexBytes `json:"signature,omitempty"`
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
		Address:     dbOrg.Address,
		Name:        dbOrg.Name,
		CreatedAt:   dbOrg.CreatedAt.Format(time.RFC3339),
		Type:        string(dbOrg.Type),
		Description: dbOrg.Description,
		Size:        dbOrg.Size,
		Color:       dbOrg.Color,
		Logo:        dbOrg.Logo,
		Subdomain:   dbOrg.Subdomain,
		Timezone:    dbOrg.Timezone,
		Active:      dbOrg.Active,
		Parent:      parentOrg,
	}
}
