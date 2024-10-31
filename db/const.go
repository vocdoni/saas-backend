package db

const (
	// user roles
	AdminRole   UserRole = "admin"
	ManagerRole UserRole = "manager"
	ViewerRole  UserRole = "viewer"
	// organization types
	AssemblyType               OrganizationType = "assembly"
	AssociationType            OrganizationType = "association"
	ChamberType                OrganizationType = "chamber"
	ReligiousType              OrganizationType = "religious"
	CityType                   OrganizationType = "city"
	CompanyType                OrganizationType = "company"
	CooperativeType            OrganizationType = "cooperative"
	PoliticalPartyType         OrganizationType = "political_party"
	EducationalInstitutionType OrganizationType = "educational"
	UnionType                  OrganizationType = "union"
	NonprofitType              OrganizationType = "nonprofit"
	CommunityType              OrganizationType = "community"
	ProfessionalCollegeType    OrganizationType = "professional_college"
	OthersType                 OrganizationType = "others"
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
	CompanyType:   true,
	CommunityType: true,
}

// OrganizationTypesNames is a map that contains the organization type names by
// type
var OrganizationTypesNames = map[OrganizationType]string{
	AssemblyType:               "Assembly",
	AssociationType:            "Association",
	ChamberType:                "Chamber",
	ReligiousType:              "Church / Religious Organization",
	CityType:                   "City / Municipality",
	CompanyType:                "Company / Corporation",
	CooperativeType:            "Cooperative",
	PoliticalPartyType:         "Political Party",
	EducationalInstitutionType: "University / Educational Institution",
	UnionType:                  "Union",
	NonprofitType:              "Nonprofit / NGO",
	CommunityType:              "Community Group",
	ProfessionalCollegeType:    "Professional College",
	OthersType:                 "Others",
}

// IsOrganizationTypeValid function checks if the organization type is valid
func IsOrganizationTypeValid(ot string) bool {
	_, valid := validOrganizationTypes[OrganizationType(ot)]
	return valid
}
