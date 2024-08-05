package api

import (
	"time"

	"go.vocdoni.io/dvote/types"
)

// Organization is the struct that represents an organization in the API
type Organization struct {
	Address     string        `json:"address"`
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Description string        `json:"description"`
	Size        uint64        `json:"size"`
	Color       string        `json:"color"`
	Logo        string        `json:"logo"`
	Subdomain   string        `json:"subdomain"`
	Timezone    string        `json:"timezone"`
	Parent      *Organization `json:"parent"`
}

// UserOrganization is the struct that represents the organization of a user in
// the API, including the role of the user in the organization.
type UserOrganization struct {
	Role         string        `json:"role"`
	Organization *Organization `json:"organization"`
}

// UserInfo is the request to register a new user.
type UserInfo struct {
	Email         string              `json:"email,omitempty"`
	Password      string              `json:"password,omitempty"`
	Address       string              `json:"address,omitempty"`
	Organizations []*UserOrganization `json:"organizations,omitempty"`
}

// LoginResponse is the response of the login request which includes the JWT token
type LoginResponse struct {
	Token    string    `json:"token"`
	Expirity time.Time `json:"expirity"`
}

// TransactionData is the struct that contains the data of a transaction to
// be signed, but also is used to return the signed transaction.
type TransactionData struct {
	TxPayload string `json:"txPayload"`
}

// MessageSignature is the struct that contains the payload and the signature.
// Its used to receive and return a signed message.
type MessageSignature struct {
	Payload   []byte         `json:"payload,omitempty"`
	Signature types.HexBytes `json:"signature,omitempty"`
}
