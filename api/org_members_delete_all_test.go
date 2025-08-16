package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
)

func TestDeleteAllOrganizationMembers(t *testing.T) {
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

	// Verify members were added
	membersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 3)

	// Test deleting all members
	deleteAllReq := &apicommon.DeleteMembersRequest{
		All: true,
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 3)

	// Verify all members were deleted
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 0)
}

func TestDeleteAllOrganizationMembersDeletesGroups(t *testing.T) {
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

	// Verify members were added
	membersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 3)

	// Create a group with two of the members
	groupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:     "Test Group",
		MemberIDs: []internal.ObjectID{membersResponse.Members[0].ID, membersResponse.Members[1].ID},
	}

	groupInfo := requestAndParseWithAssertCode[apicommon.OrganizationMemberGroupInfo](
		http.StatusOK,
		t,
		http.MethodPost,
		adminToken,
		groupReq,
		"organizations",
		orgAddress.String(),
		"groups",
	)

	// verify the group was created
	c.Assert(groupInfo.ID.IsZero(), qt.Not(qt.IsTrue))
	groupMembersResp := requestAndParse[apicommon.ListOrganizationMemberGroupResponse](t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", groupInfo.ID.String(), "members",
	)
	c.Assert(groupMembersResp.Members, qt.HasLen, 2)
	c.Assert(groupMembersResp.Pagination.CurrentPage, qt.Equals, int64(1))
	c.Assert(groupMembersResp.Pagination.TotalItems, qt.Equals, int64(2))

	// Test deleting all members
	deleteAllReq := &apicommon.DeleteMembersRequest{
		All: true,
	}
	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 3)

	// Verify all members were deleted
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 0)
	c.Assert(membersResponse.Pagination.CurrentPage, qt.Equals, int64(1))
	c.Assert(membersResponse.Pagination.TotalItems, qt.Equals, int64(0))

	// Verify that querying groups/{groupid}/members doesn't return anything weird
	{
		groupMembersResp := requestAndParse[apicommon.ListOrganizationMemberGroupResponse](t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupInfo.ID.String(), "members",
		)
		c.Assert(groupMembersResp.Members, qt.HasLen, 0)
		c.Assert(groupMembersResp.Pagination.CurrentPage, qt.Equals, int64(1))
		c.Assert(groupMembersResp.Pagination.TotalItems, qt.Equals, int64(0))
	}

	// Verify the group was NOT deleted but left empty
	groupsResponse := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResponse.Groups, qt.HasLen, 1)
	c.Assert(groupsResponse.Groups[0].MemberIDs, qt.HasLen, 0)
}

func TestDeleteAllOrganizationMembersEmpty(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Test deleting all members from an empty organization
	deleteAllReq := &apicommon.DeleteMembersRequest{
		All: true,
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 0)
}

func TestDeleteAllOrganizationMembersUnauthorized(t *testing.T) {
	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Create another user without permissions
	unauthorizedToken := testCreateUser(t, "unauthorizedpassword123")

	// Test deleting all members without proper permissions
	deleteAllReq := &apicommon.DeleteMembersRequest{
		All: true,
	}

	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodDelete, unauthorizedToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")
}

func TestDeleteSpecificMembersStillWorks(t *testing.T) {
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

	// Delete only the first member by ID
	deleteSpecificReq := &apicommon.DeleteMembersRequest{
		IDs: []internal.ObjectID{membersResponse.Members[0].ID},
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteSpecificReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 1)

	// Verify only one member remains
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 1)
}
