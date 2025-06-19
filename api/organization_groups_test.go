package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestOrganizationGroups(t *testing.T) {
	c := qt.New(t)

	// Create an admin user
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	resp, code := testRequest(t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("Admin user: %s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Get the organization to verify it exists
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Add members to the organization
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P001",
				Name:         "John Doe",
				Email:        "john.doe@example.com",
				Phone:        "+34612345678",
				Password:     "password123",
				Other: map[string]any{
					"department": "Engineering",
					"age":        30,
				},
			},
			{
				MemberNumber: "P002",
				Name:         "Jane Smith",
				Email:        "jane.smith@example.com",
				Phone:        "+34698765432",
				Password:     "password456",
				Other: map[string]any{
					"department": "Marketing",
					"age":        28,
				},
			},
		},
	}

	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		members,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the members were added
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var membersResponse apicommon.OrganizationMembersResponse
	err := parseJSON(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 2, qt.Commentf("expected 2 members"))

	// Store participant IDs for later use
	var participant1ID, participant2ID string
	for _, p := range membersResponse.Members {
		switch p.MemberNumber {
		case "P001":
			participant1ID = p.ID
		case "P002":
			participant2ID = p.ID
		}
	}
	c.Assert(participant1ID, qt.Not(qt.Equals), "", qt.Commentf("Participant 1 not found"))
	c.Assert(participant2ID, qt.Not(qt.Equals), "", qt.Commentf("Participant 2 not found"))

	// Add a third participant
	members3 := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P003",
				Name:         "Bob Johnson",
				Email:        "bob.johnson@example.com",
				Phone:        "+34611223344",
				Password:     "password789",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
		},
	}

	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		members3,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get all members to find the third participant's ID
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = parseJSON(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 3, qt.Commentf("expected 3 members"))

	var participant3ID string
	for _, p := range membersResponse.Members {
		if p.MemberNumber == "P003" {
			participant3ID = p.ID
			break
		}
	}
	c.Assert(participant3ID, qt.Not(qt.Equals), "", qt.Commentf("Participant 3 not found"))

	t.Run("CreateOrganizationMemberGroup", func(t *testing.T) {
		// Test 1: Create a new group with the two members
		createRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Test Group",
			Description: "This is a test group",
			MemberIDs:   []string{participant1ID, participant2ID},
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var groupInfo apicommon.OrganizationMemberGroupInfo
		err = parseJSON(resp, &groupInfo)
		c.Assert(err, qt.IsNil)
		c.Assert(groupInfo.ID, qt.Not(qt.Equals), "")

		// Test 2: Try to create a group without authentication
		_, code = testRequest(
			t,
			http.MethodPost,
			"",
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 3: Try to create a group with a non-admin user
		// Create a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodPost,
			nonAdminToken,
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Create a group with an invalid organization address
		_, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			createRequest,
			"organizations",
			"invalid-address",
			"groups",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 5: Create a group with invalid member IDs
		invalidRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Invalid Group",
			Description: "This group has invalid member IDs",
			MemberIDs:   []string{"invalid-id", "not-a-number"},
		}
		_, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			invalidRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Save the group ID for later tests
		groupID := groupInfo.ID

		// Test 6: Create another group with only one participant
		createRequest = &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Single Member Group",
			Description: "This group has only one member",
			MemberIDs:   []string{participant1ID},
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var singleMemberGroupInfo apicommon.OrganizationMemberGroupInfo
		err = parseJSON(resp, &singleMemberGroupInfo)
		c.Assert(err, qt.IsNil)
		c.Assert(singleMemberGroupInfo.ID, qt.Not(qt.Equals), "")
		c.Assert(singleMemberGroupInfo.ID, qt.Not(qt.Equals), groupID) // Ensure it's a different group
	})

	t.Run("GetOrganizationMemberGroups", func(t *testing.T) {
		// Test 1: Get all groups for the organization
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var groupsResponse apicommon.OrganizationMemberGroupsResponse
		err = parseJSON(resp, &groupsResponse)
		c.Assert(err, qt.IsNil)
		c.Assert(len(groupsResponse.Groups), qt.Equals, 2) // We created two groups in the previous test

		// Test 2: Try to get groups without authentication
		_, code = testRequest(
			t,
			http.MethodGet,
			"",
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 3: Try to get groups with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodGet,
			nonAdminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to get groups with an invalid organization address
		_, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			"invalid-address",
			"groups",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Save the first group ID for later tests
		firstGroupID := groupsResponse.Groups[0].ID
		c.Assert(firstGroupID, qt.Not(qt.Equals), "")

		// Test 5: Get a specific group by ID
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			firstGroupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var groupInfo apicommon.OrganizationMemberGroupInfo
		err = parseJSON(resp, &groupInfo)
		c.Assert(err, qt.IsNil)
		c.Assert(groupInfo.ID, qt.Equals, firstGroupID)
		c.Assert(groupInfo.Title, qt.Not(qt.Equals), "")
		c.Assert(groupInfo.Description, qt.Not(qt.Equals), "")
		c.Assert(len(groupInfo.MemberIDs), qt.Not(qt.Equals), 0)

		// Test 6: Try to get a non-existent group
		_, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			"nonexistentgroupid",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
	})

	t.Run("UpdateOrganizationMemberGroup", func(t *testing.T) {
		// First, get all groups to find a group ID to update
		resp, code := testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		var groupsResponse apicommon.OrganizationMemberGroupsResponse
		err := parseJSON(resp, &groupsResponse)
		c.Assert(err, qt.IsNil)
		c.Assert(len(groupsResponse.Groups), qt.Not(qt.Equals), 0)

		groupID := groupsResponse.Groups[0].ID

		// Test 1: Update the group's title and description
		updateRequest := &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:       "Updated Group Title",
			Description: "Updated group description",
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the update was successful
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		var updatedGroup apicommon.OrganizationMemberGroupInfo
		err = parseJSON(resp, &updatedGroup)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedGroup.Title, qt.Equals, "Updated Group Title")
		c.Assert(updatedGroup.Description, qt.Equals, "Updated group description")

		// Test 2: Add a participant to the group
		updateRequest = &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:       updatedGroup.Title,
			Description: updatedGroup.Description,
			AddMembers:  []string{participant3ID},
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the participant was added
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		err = parseJSON(resp, &updatedGroup)
		c.Assert(err, qt.IsNil)

		// Check if the new participant ID is in the group's member IDs
		found := false
		for _, id := range updatedGroup.MemberIDs {
			if id == participant3ID {
				found = true
				break
			}
		}
		c.Assert(found, qt.Equals, true, qt.Commentf("New participant not found in group"))

		// Test 3: Remove a participant from the group
		updateRequest = &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:         updatedGroup.Title,
			Description:   updatedGroup.Description,
			RemoveMembers: []string{participant3ID},
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the participant was removed
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		err = parseJSON(resp, &updatedGroup)
		c.Assert(err, qt.IsNil)

		// Check that the participant ID is no longer in the group's member IDs
		found = false
		for _, id := range updatedGroup.MemberIDs {
			if id == participant3ID {
				found = true
				break
			}
		}
		c.Assert(found, qt.Equals, false, qt.Commentf("Removed participant still found in group"))

		// Test 4: Try to update a group without authentication
		_, code = testRequest(
			t,
			http.MethodPut,
			"",
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 5: Try to update a group with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodPut,
			nonAdminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 6: Try to update a non-existent group
		_, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			"nonexistentgroupid",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
	})

	t.Run("ListOrganizationMemberGroupMembers", func(t *testing.T) {
		// First, get all groups to find a group ID
		resp, code := testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		var groupsResponse apicommon.OrganizationMemberGroupsResponse
		err := parseJSON(resp, &groupsResponse)
		c.Assert(err, qt.IsNil)
		c.Assert(len(groupsResponse.Groups), qt.Not(qt.Equals), 0)

		groupID := groupsResponse.Groups[0].ID

		// Test 1: List members of a group
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
			"members",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var membersResponse apicommon.ListOrganizationMemberGroupResponse
		err = parseJSON(resp, &membersResponse)
		c.Assert(err, qt.IsNil)
		// We can't assert the exact number of members since it depends on previous tests
		// but we can check that the response structure is correct
		c.Assert(membersResponse.CurrentPage, qt.Not(qt.Equals), 0)

		// Test 2: List members with pagination
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
			"members",
			"?page=1&pageSize=5",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		err = parseJSON(resp, &membersResponse)
		c.Assert(err, qt.IsNil)
		c.Assert(membersResponse.CurrentPage, qt.Equals, 1)

		// Test 3: Try to list members without authentication
		_, code = testRequest(
			t,
			http.MethodGet,
			"",
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
			"members",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to list members with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodGet,
			nonAdminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
			"members",
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 5: Try to list members of a non-existent group
		_, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			"nonexistentgroupid",
			"members",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
	})

	t.Run("DeleteOrganizationMemberGroup", func(t *testing.T) {
		// First, create a new group to delete
		createRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Group to Delete",
			Description: "This group will be deleted",
			MemberIDs:   []string{participant1ID},
		}
		resp, code := testRequest(
			t,
			http.MethodPost,
			adminToken,
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		var groupInfo apicommon.OrganizationMemberGroupInfo
		err := parseJSON(resp, &groupInfo)
		c.Assert(err, qt.IsNil)
		c.Assert(groupInfo.ID, qt.Not(qt.Equals), "")

		groupID := groupInfo.ID

		// Test 1: Delete the group
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the group was deleted by trying to get it
		_, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 2: Try to delete a group without authentication
		// First create another group
		createRequest = &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Another Group to Delete",
			Description: "This group will be used for unauthorized delete test",
			MemberIDs:   []string{participant1ID},
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			createRequest,
			"organizations",
			orgAddress.String(),
			"groups",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		err = parseJSON(resp, &groupInfo)
		c.Assert(err, qt.IsNil)
		groupID = groupInfo.ID

		_, code = testRequest(
			t,
			http.MethodDelete,
			"",
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 3: Try to delete a group with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodDelete,
			nonAdminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to delete a non-existent group
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			"nonexistentgroupid",
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Clean up: Delete the group created for this test
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
		)
		c.Assert(code, qt.Equals, http.StatusOK)
	})

	// Clean up: Delete the members
	deleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{
			membersResponse.Members[0].ID,
			membersResponse.Members[1].ID,
			membersResponse.Members[2].ID,
		},
	}
	resp, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		deleteRequest,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK)
}
