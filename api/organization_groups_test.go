package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

func TestOrganizationGroups(t *testing.T) {
	c := qt.New(t)

	// Create an admin user
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	adminUser := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	t.Logf("Admin user: %+v\n", adminUser)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Get the organization to verify it exists
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())

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

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members")

	// Verify the members were added
	membersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 2, qt.Commentf("expected 2 members"))

	membersMap := make(map[string]apicommon.OrgMember)
	// Store participant IDs for later use
	for _, p := range membersResponse.Members {
		membersMap[p.MemberNumber] = p
	}
	c.Assert(membersMap["P001"], qt.Not(qt.Equals), "", qt.Commentf("Participant 1 not found"))
	c.Assert(membersMap["P002"], qt.Not(qt.Equals), "", qt.Commentf("Participant 2 not found"))

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

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, members3,
		"organizations", orgAddress.String(), "members")

	// Get all members to find the third participant's ID
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 3, qt.Commentf("expected 3 members"))

	// Store participant IDs for later use
	for _, p := range membersResponse.Members {
		membersMap[p.MemberNumber] = p
	}
	c.Assert(membersMap["P003"], qt.Not(qt.Equals), "", qt.Commentf("Participant 3 not found"))

	t.Run("CreateOrganizationMemberGroup", func(t *testing.T) {
		// Test 1: Create a new group with the two members
		createRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Test Group",
			Description: "This is a test group",
			MemberIDs:   []string{membersMap["P001"].ID, membersMap["P002"].ID},
		}
		groupInfo := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodPost, adminToken, createRequest,
			"organizations", orgAddress.String(), "groups")
		c.Assert(groupInfo.ID, qt.Not(qt.Equals), "")

		// Test 2: Try to create a group without authentication
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPost, "", createRequest,
			"organizations", orgAddress.String(), "groups")

		// Test 3: Try to create a group with a non-admin user
		// Create a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPost, nonAdminToken, createRequest,
			"organizations", orgAddress.String(), "groups")

		// Test 4: Create a group with an invalid organization address
		requestAndAssertCode(http.StatusBadRequest,
			t, http.MethodPost, adminToken, createRequest,
			"organizations", "invalid-address", "groups")

		// Test 5: Create a group with invalid member IDs
		invalidRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Invalid Group",
			Description: "This group has invalid member IDs",
			MemberIDs:   []string{"invalid-id", "not-a-number"},
		}
		requestAndAssertCode(http.StatusInternalServerError,
			t, http.MethodPost, adminToken, invalidRequest,
			"organizations", orgAddress.String(), "groups")

		// Save the group ID for later tests
		groupID := groupInfo.ID

		// Test 6: Create another group with only one participant
		createRequest = &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Single Member Group",
			Description: "This group has only one member",
			MemberIDs:   []string{membersMap["P001"].ID},
		}
		singleMemberGroupInfo := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodPost, adminToken, createRequest,
			"organizations", orgAddress.String(), "groups")
		c.Assert(singleMemberGroupInfo.ID, qt.Not(qt.Equals), "")
		c.Assert(singleMemberGroupInfo.ID, qt.Not(qt.Equals), groupID) // Ensure it's a different group
	})

	t.Run("GetOrganizationMemberGroups", func(t *testing.T) {
		// Test 1: Get all groups for the organization
		groupsResponse := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups")
		c.Assert(groupsResponse.Groups, qt.HasLen, 2) // We created two groups in the previous test (Test 1 and Test 6)

		// Test 2: Try to get groups without authentication
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodGet, "", nil,
			"organizations", orgAddress.String(), "groups")

		// Test 3: Try to get groups with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodGet, nonAdminToken, nil,
			"organizations", orgAddress.String(), "groups")

		// Test 4: Try to get groups with an invalid organization address
		requestAndAssertCode(http.StatusBadRequest,
			t, http.MethodGet, adminToken, nil,
			"organizations", "invalid-address", "groups")

		// Save the first group ID for later tests
		firstGroupID := groupsResponse.Groups[0].ID
		c.Assert(firstGroupID, qt.Not(qt.Equals), "")

		// Test 5: Get a specific group by ID
		groupInfo := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", firstGroupID)
		c.Assert(groupInfo.ID, qt.Equals, firstGroupID)
		c.Assert(groupInfo.Title, qt.Not(qt.Equals), "")
		c.Assert(groupInfo.Description, qt.Not(qt.Equals), "")
		c.Assert(len(groupInfo.MemberIDs), qt.Not(qt.Equals), 0)

		// Test 6: Try to get a non-existent group
		requestAndAssertCode(http.StatusInternalServerError,
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", "nonexistentgroupid")
	})

	t.Run("UpdateOrganizationMemberGroup", func(t *testing.T) {
		// First, get all groups to find a group ID to update
		groupsResponse := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups")
		c.Assert(len(groupsResponse.Groups), qt.Not(qt.Equals), 0)

		groupID := groupsResponse.Groups[0].ID

		// Test 1: Update the group's title and description
		updateRequest := &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:       "Updated Group Title",
			Description: "Updated group description",
		}
		requestAndAssertCode(http.StatusOK,
			t, http.MethodPut, adminToken, updateRequest,
			"organizations", orgAddress.String(), "groups", groupID)

		// Verify the update was successful
		updatedGroup := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)
		c.Assert(updatedGroup.Title, qt.Equals, "Updated Group Title")
		c.Assert(updatedGroup.Description, qt.Equals, "Updated group description")

		// Test 2: Add a participant to the group
		updateRequest = &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:       updatedGroup.Title,
			Description: updatedGroup.Description,
			AddMembers:  []string{membersMap["P003"].ID},
		}
		requestAndAssertCode(http.StatusOK,
			t, http.MethodPut, adminToken, updateRequest,
			"organizations", orgAddress.String(), "groups", groupID)

		// Verify the participant was added
		updatedGroup = requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Check if the new participant ID is in the group's member IDs
		found := false
		for _, id := range updatedGroup.MemberIDs {
			if id == membersMap["P003"].ID {
				found = true
				break
			}
		}
		c.Assert(found, qt.Equals, true, qt.Commentf("New participant not found in group"))

		// Test 3: Remove a participant from the group
		updateRequest = &apicommon.UpdateOrganizationMemberGroupsRequest{
			Title:         updatedGroup.Title,
			Description:   updatedGroup.Description,
			RemoveMembers: []string{membersMap["P003"].ID},
		}
		requestAndAssertCode(http.StatusOK,
			t, http.MethodPut, adminToken, updateRequest,
			"organizations", orgAddress.String(), "groups", groupID)

		// Verify the participant was removed
		updatedGroup = requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Check that the participant ID is no longer in the group's member IDs
		found = false
		for _, id := range updatedGroup.MemberIDs {
			if id == membersMap["P003"].ID {
				found = true
				break
			}
		}
		c.Assert(found, qt.Equals, false, qt.Commentf("Removed participant still found in group"))

		// Test 4: Try to update a group without authentication
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPut, "", updateRequest,
			"organizations", orgAddress.String(), "groups", groupID)

		// Test 5: Try to update a group with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPut, nonAdminToken, updateRequest,
			"organizations", orgAddress.String(), "groups", groupID)

		// Test 6: Try to update a non-existent group
		requestAndAssertCode(http.StatusInternalServerError,
			t, http.MethodPut, adminToken, updateRequest,
			"organizations", orgAddress.String(), "groups", "nonexistentgroupid")
	})

	t.Run("ListOrganizationMemberGroupMembers", func(t *testing.T) {
		// First, get all groups to find a group ID
		groupsResponse := requestAndParse[apicommon.OrganizationMemberGroupsResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups")
		c.Assert(len(groupsResponse.Groups), qt.Not(qt.Equals), 0)

		groupID := groupsResponse.Groups[0].ID

		// Test 1: List members of a group
		membersResponse := requestAndParse[apicommon.ListOrganizationMemberGroupResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID, "members")
		// We can't assert the exact number of members since it depends on previous tests
		// but we can check that the response structure is correct
		c.Assert(membersResponse.CurrentPage, qt.Not(qt.Equals), 0)

		// Test 2: List members with pagination
		membersResponse = requestAndParse[apicommon.ListOrganizationMemberGroupResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID, "members", "?page=1&pageSize=5")
		c.Assert(membersResponse.CurrentPage, qt.Equals, 1)

		// Test 3: Try to list members without authentication
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodGet, "", nil,
			"organizations", orgAddress.String(), "groups", groupID, "members")

		// Test 4: Try to list members with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodGet, nonAdminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID, "members")

		// Test 5: Try to list members of a non-existent group
		requestAndAssertCode(http.StatusInternalServerError,
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", "nonexistentgroupid", "members")
	})

	t.Run("DeleteOrganizationMemberGroup", func(t *testing.T) {
		// First, create a new group to delete
		createRequest := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Group to Delete",
			Description: "This group will be deleted",
			MemberIDs:   []string{membersMap["P001"].ID},
		}
		groupInfo := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodPost, adminToken, createRequest,
			"organizations", orgAddress.String(), "groups")
		c.Assert(groupInfo.ID, qt.Not(qt.Equals), "")

		groupID := groupInfo.ID

		// Test 1: Delete the group
		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Verify the group was deleted by trying to get it
		requestAndAssertCode(http.StatusBadRequest,
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Test 2: Try to delete a group without authentication
		// First create another group
		createRequest = &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Another Group to Delete",
			Description: "This group will be used for unauthorized delete test",
			MemberIDs:   []string{membersMap["P001"].ID},
		}
		groupInfo = requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodPost, adminToken, createRequest,
			"organizations", orgAddress.String(), "groups")
		groupID = groupInfo.ID

		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodDelete, "", nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Test 3: Try to delete a group with a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodDelete, nonAdminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)

		// Test 4: Try to delete a non-existent group
		requestAndAssertCode(http.StatusInternalServerError,
			t, http.MethodDelete, adminToken, nil,
			"organizations", orgAddress.String(), "groups", "nonexistentgroupid")

		// Clean up: Delete the group created for this test
		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)
	})

	t.Run("ValidateOrganizationMemberGroup", func(t *testing.T) {
		c := qt.New(t)

		// First, add members with duplicate member numbers to test validation
		duplicateMembers := &apicommon.AddMembersRequest{
			Members: []apicommon.OrgMember{
				{
					MemberNumber: "P007", // Same member number
					Name:         "Duplicate User7 A",
					Email:        "duplicate7a@example.com",
					Phone:        "+34698777111",
					Password:     "password7a",
				},
				{
					MemberNumber: "P007", // Same member number
					Name:         "Duplicate User7 B",
					Email:        "duplicate7b@example.com",
					Phone:        "+34698777222",
					Password:     "password7b",
				},
			},
		}

		// Add duplicate members to the organization
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, duplicateMembers,
			"organizations", orgAddress.String(), "members")

		groupID := createGroupWithAllCurrentMembers(t, adminToken, orgAddress.String())

		// Test 1: Validate with valid auth fields (should succeed)
		validRequest := &apicommon.ValidateMemberGroupRequest{
			AuthFields: db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
			},
		}
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, validRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Test 2: Validate with valid two-factor fields (should succeed)
		validTwoFaRequest := &apicommon.ValidateMemberGroupRequest{
			TwoFaFields: db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
			},
		}
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, validTwoFaRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Test 3: Validate with both auth fields and two-factor fields (should succeed)
		combinedRequest := &apicommon.ValidateMemberGroupRequest{
			AuthFields: db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
			},
			TwoFaFields: db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldPhone,
			},
		}
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, combinedRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Add a member with empty phone to test validation
		emptyFieldMember := &apicommon.AddMembersRequest{
			Members: []apicommon.OrgMember{
				{
					MemberNumber: "P008",
					Name:         "Empty Phone User",
					Email:        "p0008@mail.com",
					Phone:        "", // Empty phone
					Password:     "password888",
				},
			},
		}

		// Add member with empty field to the organization
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, emptyFieldMember,
			"organizations", orgAddress.String(), "members")

		groupID = createGroupWithAllCurrentMembers(t, adminToken, orgAddress.String())

		// Test 4: Validate with duplicate auth field (should fail)
		duplicateRequest := &apicommon.ValidateMemberGroupRequest{
			AuthFields: db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsMemberNumber, // This will have duplicates
			},
		}
		duplicateResponse := requestAndParseWithAssertCode[map[string]any](http.StatusBadRequest, t,
			http.MethodPost, adminToken, duplicateRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate",
		)

		// The response should contain information about the duplicates
		aggregationResults := decodeNestedFieldAs[db.OrgMemberAggregationResults](c, duplicateResponse, "data")
		c.Assert(
			len(aggregationResults.Duplicates) > 0,
			qt.Equals,
			true,
			qt.Commentf("Expected duplicates in aggregationResults: %+v", aggregationResults),
		)

		// Test 5: Validate with empty field (should fail)
		emptyFieldRequest := &apicommon.ValidateMemberGroupRequest{
			TwoFaFields: db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldPhone, // One member has empty phone
			},
		}
		emptyFieldResponse := requestAndParseWithAssertCode[map[string]any](
			http.StatusBadRequest,
			t,
			http.MethodPost,
			adminToken,
			emptyFieldRequest,
			"organizations",
			orgAddress.String(),
			"groups",
			groupID,
			"validate",
		)

		// The response should contain information about the empty fields
		aggregationResults = decodeNestedFieldAs[db.OrgMemberAggregationResults](c, emptyFieldResponse, "data")
		c.Assert(aggregationResults.MissingData, qt.HasLen, 1,
			qt.Commentf("Expected missing data in aggregationResults: %+v", aggregationResults),
		)

		// Test 6: Validate with neither auth fields nor two-factor fields (should fail)
		emptyRequest := &apicommon.ValidateMemberGroupRequest{}
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, emptyRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Test 7: Validate without authentication (should fail)
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", validRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Test 8: Validate with non-admin user (should fail)
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, nonAdminToken, validRequest,
			"organizations", orgAddress.String(), "groups", groupID, "validate")

		// Test 9: Validate with invalid group ID (should fail)
		requestAndAssertCode(http.StatusInternalServerError, t, http.MethodPost, adminToken, validRequest,
			"organizations", orgAddress.String(), "groups", "nonexistentgroupid", "validate")

		// Clean up: Delete the group created for this test
		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil,
			"organizations", orgAddress.String(), "groups", groupID)
	})

	// Clean up: Delete the members
	deleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{
			membersResponse.Members[0].ID,
			membersResponse.Members[1].ID,
			membersResponse.Members[2].ID,
		},
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, deleteRequest,
		"organizations", orgAddress.String(), "members")
}

func createGroupWithAllCurrentMembers(t *testing.T, adminToken, orgAddress string) string {
	// Get all members to create a group with them
	allMembersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress, "members")

	// Create a group with all members
	var allMemberIDs []string
	for _, member := range allMembersResponse.Members {
		allMemberIDs = append(allMemberIDs, member.ID)
	}

	testGroupRequest := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "Validation Test Group",
		Description: "A group for testing validation",
		MemberIDs:   allMemberIDs,
	}

	testGroup := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, adminToken, testGroupRequest,
		"organizations", orgAddress, "groups")
	qt.Assert(t, testGroup.ID, qt.Not(qt.Equals), "")

	return testGroup.ID
}
