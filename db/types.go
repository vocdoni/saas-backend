package db

import "time"

type User struct {
	ID            uint64               `json:"id" bson:"_id"`
	Email         string               `json:"email" bson:"email"`
	Password      string               `json:"password" bson:"password"`
	FirstName     string               `json:"firstName" bson:"firstName"`
	LastName      string               `json:"lastName" bson:"lastName"`
	Organizations []OrganizationMember `json:"organizations" bson:"organizations"`
}

func (u *User) HasRoleFor(address string, role UserRole) bool {
	for _, org := range u.Organizations {
		if org.Address == address && string(org.Role) == string(role) {
			return true
		}
	}
	return false
}

type UserRole string

type OrganizationType string

type OrganizationMember struct {
	Address string   `json:"address" bson:"_id"`
	Role    UserRole `json:"role" bson:"role"`
}

type Organization struct {
	Address         string           `json:"address" bson:"_id"`
	Name            string           `json:"name" bson:"name"`
	Website         string           `json:"website" bson:"website"`
	Type            OrganizationType `json:"type" bson:"type"`
	Creator         string           `json:"creator" bson:"creator"`
	CreatedAt       time.Time        `json:"createdAt" bson:"createdAt"`
	Nonce           string           `json:"nonce" bson:"nonce"`
	Description     string           `json:"description" bson:"description"`
	Size            string           `json:"size" bson:"size"`
	Color           string           `json:"color" bson:"color"`
	Logo            string           `json:"logo" bson:"logo"`
	Header          string           `json:"header" bson:"header"`
	Subdomain       string           `json:"subdomain" bson:"subdomain"`
	Timezone        string           `json:"timezone" bson:"timezone"`
	Language        string           `json:"language" bson:"language"`
	Active          bool             `json:"active" bson:"active"`
	TokensPurchased uint64           `json:"tokensPurchased" bson:"tokensPurchased"`
	TokensRemaining uint64           `json:"tokensRemaining" bson:"tokensRemaining"`
	Parent          string           `json:"parent" bson:"parent"`
}
