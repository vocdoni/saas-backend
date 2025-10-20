package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestCreateGroupWithAllMembers(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Add some test members to the organization
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "001",
				Name:         "John",
				Surname:      "Doe",
				Email:        "john.doe@example.com",
			},
			{
				MemberNumber: "002",
				Name:         "Jane",
				Surname:      "Smith",
				Email:        "jane.smith@example.com",
			},
			{
				MemberNumber: "003",
				Name:         "Bob",
				Surname:      "Johnson",
				Email:        "bob.johnson@example.com",
			},
		},
	}

	// Add members to the organization
	addedResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members",
	)
	c.Assert(addedResponse.Added, qt.Equals, uint32(3))

	// Create a group with all members
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:             "All Members Group",
		Description:       "A group containing all organization members",
		IncludeAllMembers: true,
	}

	groupResponse := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, adminToken, createGroupReq,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupResponse.ID, qt.Not(qt.Equals), "")

	// Get the group details to verify all members were included
	groupDetails := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", groupResponse.ID)
	c.Assert(groupDetails.MemberIDs, qt.HasLen, 3)
	c.Assert(groupDetails.Title, qt.Equals, "All Members Group")
	c.Assert(groupDetails.Description, qt.Equals, "A group containing all organization members")
}

func TestCreateGroupWithAllMembersEmpty(t *testing.T) {
	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Create a group with all members from an empty organization
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:             "Empty Group",
		Description:       "A group from an empty organization",
		IncludeAllMembers: true,
	}

	// Should fail with "could not create organization member group: invalid data provided"
	// TODO: check actual error
	requestAndAssertCode(http.StatusInternalServerError,
		t, http.MethodPost, adminToken, createGroupReq,
		"organizations", orgAddress.String(), "groups")
}

func TestCreateGroupWithAllMembersUnauthorized(t *testing.T) {
	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Create another user without permissions
	unauthorizedToken := testCreateUser(t, "unauthorizedpassword123")

	// Try to create a group with all members without proper permissions
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:             "Unauthorized Group",
		Description:       "Should fail",
		IncludeAllMembers: true,
	}

	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodPost, unauthorizedToken, createGroupReq,
		"organizations", orgAddress.String(), "groups")
}

func TestCreateGroupSpecificMembersStillWorks(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Add some test members to the organization
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "001",
				Name:         "John",
				Surname:      "Doe",
				Email:        "john.doe@example.com",
			},
			{
				MemberNumber: "002",
				Name:         "Jane",
				Surname:      "Smith",
				Email:        "jane.smith@example.com",
			},
		},
	}

	// Add members to the organization
	addedResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members",
	)
	c.Assert(addedResponse.Added, qt.Equals, uint32(2))

	// Get the member IDs
	membersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 2)

	// Create a group with only the first member
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "Specific Members Group",
		Description: "A group with specific members",
		MemberIDs:   []string{membersResponse.Members[0].ID},
	}

	groupResponse := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, adminToken, createGroupReq,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupResponse.ID, qt.Not(qt.Equals), "")

	// Get the group details to verify only one member was included
	groupDetails := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", groupResponse.ID)
	c.Assert(groupDetails.MemberIDs, qt.HasLen, 1)
	c.Assert(groupDetails.MemberIDs[0], qt.Equals, membersResponse.Members[0].ID)
}

func TestCreateGroupWithLargeNumberOfMembers(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Add a larger number of test members to the organization
	var testMembers []apicommon.OrgMember
	for i := 1; i <= 50; i++ {
		testMembers = append(testMembers, apicommon.OrgMember{
			MemberNumber: string(rune('0'+(i%10))) + string(rune('0'+((i/10)%10))) + string(rune('0'+((i/100)%10))),
			Name:         "Member",
			Surname:      string(rune('A' + (i % 26))),
			Email:        "member" + string(rune('0'+(i%10))) + "@example.com",
		})
	}

	members := &apicommon.AddMembersRequest{
		Members: testMembers,
	}

	// Add members to the organization
	addedResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members",
	)
	c.Assert(addedResponse.Added, qt.Equals, uint32(50))

	// Create a group with all members
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:             "Large Group",
		Description:       "A group with many members",
		IncludeAllMembers: true,
	}

	groupResponse := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, adminToken, createGroupReq,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupResponse.ID, qt.Not(qt.Equals), "")

	// Get the group details to verify all members were included
	groupDetails := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", groupResponse.ID)
	c.Assert(groupDetails.MemberIDs, qt.HasLen, 50)
}
