package db

type User struct {
	ID            uint64               `json:"id" bson:"_id"`
	Email         string               `json:"email" bson:"email"`       // unique (email format)
	Password      string               `json:"password" bson:"password"` // hashed not null
	Organizations []OrganizationMember `json:"organizations" bson:"organizations"`
}

type UserRole string

type OrganizationType string

type OrganizationMember struct {
	Address string   `json:"address" bson:"_id"`
	Role    UserRole `json:"role" bson:"role"`
}

type Organization struct {
	Address         string           `json:"address" bson:"_id"`
	Name            string           `json:"name" bson:"name"` // unique
	Type            OrganizationType `json:"type" bson:"type"`
	Description     string           `json:"description" bson:"description"`
	Size            uint64           `json:"size" bson:"size"`
	Color           string           `json:"color" bson:"color"`
	Logo            string           `json:"logo" bson:"logo"`
	Subdomain       string           `json:"subdomain" bson:"subdomain"`
	Timezone        string           `json:"timezone" bson:"timezone"`
	Parent          string           `json:"parent" bson:"parent"`
	TokensPurchased uint64           `json:"tokensPurchased" bson:"tokensPurchased"`
	TokensRemaining uint64           `json:"tokensRemaining" bson:"tokensRemaining"`
}