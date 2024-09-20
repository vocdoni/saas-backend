package db

const (
	// user roles
	AdminRole   UserRole = "admin"
	ManagerRole UserRole = "manager"
	ViewerRole  UserRole = "viewer"
	// organization types
	CompanyType   OrganizationType = "company"
	CommunityType OrganizationType = "community"
	// verification code types
	CodeTypeAccountVerification CodeType = "account"
	CodeTypePasswordReset       CodeType = "password"
)

// writableRoles is a map that contains if the role is writable or not
var writableRoles = map[UserRole]bool{
	AdminRole:   true,
	ManagerRole: true,
	ViewerRole:  false,
}

// HasWriteAccess function checks if the user role has write access
func HasWriteAccess(role UserRole) bool {
	return writableRoles[role]
}

// validOrganizationTypes is a map that contains the valid organization types
var validOrganizationTypes = map[OrganizationType]bool{
	CompanyType:   true,
	CommunityType: true,
}

// IsOrganizationTypeValid function checks if the organization type is valid
func IsOrganizationTypeValid(ot string) bool {
	_, valid := validOrganizationTypes[OrganizationType(ot)]
	return valid
}
