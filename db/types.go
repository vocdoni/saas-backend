package db

type User struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserRole string

type OrganizationType string

type OrganizationMember struct {
	User *User    `json:"user"`
	Role UserRole `json:"role"`
}

type Organization struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Type        OrganizationType      `json:"type"`
	Description string                `json:"description"`
	Members     []*OrganizationMember `json:"members"`
	Size        uint64                `json:"size"`
	Color       string                `json:"color"`
	Logo        string                `json:"logo"`
	Subdomain   string                `json:"subdomain"`
	Timezone    string                `json:"timezone"`
	Parent      *Organization         `json:"parent"`
}
