package db

const (
	// user roles
	AdminRole   UserRole = "admin"
	ManagerRole UserRole = "manager"
	ViewerRole  UserRole = "viewer"
	// organization types
	AssociationType      OrganizationType = "association"
	CompanyType          OrganizationType = "company"
	CooperativeType      OrganizationType = "cooperative"
	GovernmentType       OrganizationType = "government"
	OthersType           OrganizationType = "others"
	PoliticalPartyType   OrganizationType = "political_party"
	ProfessionalBodyType OrganizationType = "professional_body"
	SportsClubType       OrganizationType = "sports_club"
	UnionType            OrganizationType = "union"
	// verification code types
	CodeTypeVerifyAccount   CodeType = "verify_account"
	CodeTypePasswordReset   CodeType = "password_reset"
	CodeTypeOrgInvite       CodeType = "organization_invite"
	CodeTypeOrgInviteUpdate CodeType = "organization_invite_update"
)

// writableRoles is a map that contains if the role is writable or not
var writableRoles = map[UserRole]bool{
	AdminRole:   true,
	ManagerRole: true,
	ViewerRole:  false,
}

// UserRoleNames is a map that contains the user role names by role
var UserRolesNames = map[UserRole]string{
	AdminRole:   "Admin",
	ManagerRole: "Manager",
	ViewerRole:  "Viewer",
}

// HasWriteAccess function checks if the user role has write access
func HasWriteAccess(role UserRole) bool {
	return writableRoles[role]
}

// validOrganizationTypes is a map that contains the valid organization types
var validOrganizationTypes = map[OrganizationType]bool{
	AssociationType:      true,
	CompanyType:          true,
	CooperativeType:      true,
	GovernmentType:       true,
	OthersType:           true,
	PoliticalPartyType:   true,
	ProfessionalBodyType: true,
	SportsClubType:       true,
	UnionType:            true,
}

// OrganizationTypesNames is a map that contains the organization type names by
// type
var OrganizationTypesNames = map[OrganizationType]string{
	AssociationType:      "Association",
	CompanyType:          "Company / Corporation",
	CooperativeType:      "Cooperative",
	GovernmentType:       "Government",
	PoliticalPartyType:   "Political Party",
	ProfessionalBodyType: "Professional Body",
	SportsClubType:       "Sports Club",
	UnionType:            "Union",
	OthersType:           "Others",
}

// IsOrganizationTypeValid function checks if the organization type is valid
func IsOrganizationTypeValid(ot string) bool {
	_, valid := validOrganizationTypes[OrganizationType(ot)]
	return valid
}

// ValidRoles is a map that contains the valid user roles
var validRoles = map[UserRole]bool{
	AdminRole:   true,
	ManagerRole: true,
	ViewerRole:  true,
}

// IsValidUserRole function checks if the user role is valid
func IsValidUserRole(role UserRole) bool {
	_, valid := validRoles[role]
	return valid
}
