package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestAutoGroupCreatedOnMemberUpload verifies that uploading members automatically
// creates the "All members" auto group as the first group in the listing.
func TestAutoGroupCreatedOnMemberUpload(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	// No members yet — no auto group should appear.
	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 0)

	// Add members via bulk upload.
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	// Auto group must now exist and be the first (and only) group.
	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)

	autoGroup := groupsResp.Groups[0]
	c.Assert(autoGroup.IsAutoGroup, qt.IsTrue)
	c.Assert(autoGroup.Title, qt.Equals, "All members")
	c.Assert(autoGroup.MembersCount, qt.Equals, 3)
}

// TestAutoGroupCreatedOnSingleMemberAdd verifies the auto group is created when a
// single member is added via the upsert endpoint.
func TestAutoGroupCreatedOnSingleMemberAdd(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	// Upsert a single member.
	member := apicommon.OrgMember{
		MemberNumber: "001",
		Name:         "Alice",
		Surname:      "Smith",
		Email:        "alice@example.com",
	}
	requestAndAssertCode(http.StatusOK,
		t, http.MethodPut, adminToken, &member,
		"organizations", orgAddress.String(), "members")

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)
	c.Assert(groupsResp.Groups[0].IsAutoGroup, qt.IsTrue)
	c.Assert(groupsResp.Groups[0].MembersCount, qt.Equals, 1)
}

// TestAutoGroupAlwaysMirrorsFullMemberbase verifies that the auto group member count
// stays in sync as members are added and removed.
func TestAutoGroupAlwaysMirrorsFullMemberbase(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(5)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)
	autoGroupID := groupsResp.Groups[0].ID
	c.Assert(groupsResp.Groups[0].MembersCount, qt.Equals, 5)

	// Fetch the single group details — MembersCount must reflect all 5.
	groupDetail := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", autoGroupID)
	c.Assert(groupDetail.MembersCount, qt.Equals, 5)

	// Add 2 more members.
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups[0].MembersCount, qt.Equals, 7)

	// Remove specific members.
	allMembers := getOrgMembers(t, adminToken, orgAddress)
	toDelete := []string{allMembers.Members[0].ID, allMembers.Members[1].ID}
	deleteReq := &apicommon.DeleteMembersRequest{IDs: toDelete}
	requestAndAssertCode(http.StatusOK,
		t, http.MethodDelete, adminToken, deleteReq,
		"organizations", orgAddress.String(), "members")

	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups[0].MembersCount, qt.Equals, 5)
}

// TestAutoGroupPaginatedMembersList verifies that the paginated members endpoint
// works correctly for the auto group.
func TestAutoGroupPaginatedMembersList(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(4)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	autoGroupID := groupsResp.Groups[0].ID

	membersResp := requestAndParse[apicommon.ListOrganizationMemberGroupResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", autoGroupID, "members")
	c.Assert(membersResp.Members, qt.HasLen, 4)
}

// TestAutoGroupCannotBeDeleted verifies that attempting to delete the auto group
// returns 403 Forbidden.
func TestAutoGroupCannotBeDeleted(t *testing.T) {
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	autoGroupID := groupsResp.Groups[0].ID

	requestAndAssertCode(http.StatusForbidden,
		t, http.MethodDelete, adminToken, nil,
		"organizations", orgAddress.String(), "groups", autoGroupID)
}

// TestAutoGroupMembersCannotBeManuallyModified verifies that trying to add or
// remove members from the auto group returns 403 Forbidden.
func TestAutoGroupMembersCannotBeManuallyModified(t *testing.T) {
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	autoGroupID := groupsResp.Groups[0].ID

	allMembers := getOrgMembers(t, adminToken, orgAddress)

	updateReq := &apicommon.UpdateOrganizationMemberGroupsRequest{
		RemoveMembers: []string{allMembers.Members[0].ID},
	}
	requestAndAssertCode(http.StatusForbidden,
		t, http.MethodPut, adminToken, updateReq,
		"organizations", orgAddress.String(), "groups", autoGroupID)
}

// TestAutoGroupTitleDescriptionCanBeUpdated verifies that updating only the
// title/description of the auto group is allowed.
func TestAutoGroupTitleDescriptionCanBeUpdated(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(1)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	autoGroupID := groupsResp.Groups[0].ID

	updateReq := &apicommon.UpdateOrganizationMemberGroupsRequest{
		Title:       "My Renamed All-Members Group",
		Description: "Custom description",
	}
	requestAndAssertCode(http.StatusOK,
		t, http.MethodPut, adminToken, updateReq,
		"organizations", orgAddress.String(), "groups", autoGroupID)

	groupDetail := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups", autoGroupID)
	c.Assert(groupDetail.Title, qt.Equals, "My Renamed All-Members Group")
	c.Assert(groupDetail.Description, qt.Equals, "Custom description")
	c.Assert(groupDetail.IsAutoGroup, qt.IsTrue)
}

// TestAutoGroupDisappearsWhenAllMembersDeleted verifies that deleting all members
// removes the auto group entirely, and it reappears when a member is re-added.
func TestAutoGroupDisappearsWhenAllMembersDeleted(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	// Auto group exists.
	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)

	// Delete all members.
	deleteAllReq := &apicommon.DeleteMembersRequest{All: true}
	requestAndAssertCode(http.StatusOK,
		t, http.MethodDelete, adminToken, deleteAllReq,
		"organizations", orgAddress.String(), "members")

	// Auto group should be gone.
	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 0)

	// Re-add a member — auto group should reappear.
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(1)...)

	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)
	c.Assert(groupsResp.Groups[0].IsAutoGroup, qt.IsTrue)
	c.Assert(groupsResp.Groups[0].MembersCount, qt.Equals, 1)
}

// TestAutoGroupDisappearsWhenLastMemberDeleted verifies that deleting the final
// member one-by-one removes the auto group.
func TestAutoGroupDisappearsWhenLastMemberDeleted(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(1)...)

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 1)

	memberID := getOrgMembers(t, adminToken, orgAddress).Members[0].ID
	deleteReq := &apicommon.DeleteMembersRequest{IDs: []string{memberID}}
	requestAndAssertCode(http.StatusOK,
		t, http.MethodDelete, adminToken, deleteReq,
		"organizations", orgAddress.String(), "members")

	groupsResp = requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")
	c.Assert(groupsResp.Groups, qt.HasLen, 0)
}

// TestAutoGroupAppearsFirstInPagination verifies that when multiple groups exist,
// the auto group is always the first element on page 1.
func TestAutoGroupAppearsFirstInPagination(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	allMembersResp := getOrgMembers(t, adminToken, orgAddress)
	memberIDs := make([]string, len(allMembersResp.Members))
	for i, m := range allMembersResp.Members {
		memberIDs[i] = m.ID
	}

	// Create two regular groups.
	for _, title := range []string{"Group A", "Group B"} {
		createReq := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:     title,
			MemberIDs: memberIDs[:1],
		}
		requestAndAssertCode(http.StatusOK,
			t, http.MethodPost, adminToken, createReq,
			"organizations", orgAddress.String(), "groups")
	}

	groupsResp := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "groups")

	// Total: 1 auto + 2 regular = 3.
	c.Assert(groupsResp.Pagination.TotalItems, qt.Equals, int64(3))
	// First element must be the auto group.
	c.Assert(groupsResp.Groups[0].IsAutoGroup, qt.IsTrue)
}
