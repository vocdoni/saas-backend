package db

const (
	// user roles
	AdminRole   UserRole = "admin"
	ManagerRole UserRole = "manager"
	ViewerRole  UserRole = "viewer"
	// organization types
	CompanyType   OrganizationType = "company"
	CommunityType OrganizationType = "community"
)

var validOrganizationTypes = map[OrganizationType]bool{
	CompanyType:   true,
	CommunityType: true,
}

func IsOrganizationTypeValid(ot string) bool {
	_, valid := validOrganizationTypes[OrganizationType(ot)]
	return valid
}
