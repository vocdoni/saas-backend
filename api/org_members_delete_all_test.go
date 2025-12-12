package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestDeleteAllOrganizationMembers(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	// Test deleting all members
	deleteAllReq := &apicommon.DeleteMembersRequest{
		All: true,
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 3)

	// Verify all members were deleted
	membersResponse := getOrgMembers(t, adminToken, orgAddress)
	c.Assert(membersResponse.Members, qt.HasLen, 0)
}

func TestDeleteAllOrganizationMembersDeletesGroups(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	// Verify members were added
	membersResponse := getOrgMembers(t, adminToken, orgAddress)
	c.Assert(membersResponse.Members, qt.HasLen, 3)

	// Create a group with two of the members
	groupInfo := postGroup(t, adminToken, orgAddress, membersResponse.Members[0].ID, membersResponse.Members[1].ID)

	// verify the group was created
	groupMembersResp := requestAndParse[apicommon.ListOrganizationMemberGroupResponse](t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", groupInfo.ID, "members",
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
			"organizations", orgAddress.String(), "groups", groupInfo.ID, "members",
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
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	orgMembers := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	// Delete only the first member by ID
	deleteSpecificReq := &apicommon.DeleteMembersRequest{
		IDs: []string{orgMembers[0].ID},
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteSpecificReq,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 1)

	// Verify only one member remains
	membersResponse := getOrgMembers(t, adminToken, orgAddress)
	c.Assert(membersResponse.Members, qt.HasLen, 1)
}
