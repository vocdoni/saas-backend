package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

func TestCensus(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	resp, code := testRequest(t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("Admin user: %s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress)

	// Get the organization to verify it exists
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// First, add some organization members to test with
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P001",
				Name:         "Alice Doe",
				Email:        "alice.doe@example.com",
				Phone:        "+34111111111",
				Password:     "password111",
				Other: map[string]any{
					"department": "Engineering",
					"age":        30,
				},
			},
			{
				MemberNumber: "P002",
				Name:         "Bob Smith",
				Email:        "bob.smith@example.com",
				Phone:        "+34222222222",
				Password:     "password222",
				Other: map[string]any{
					"department": "Marketing",
					"age":        28,
				},
			},
		},
	}

	// Add members to the organization first
	resp, code = testRequest(t, http.MethodPost, adminToken, members, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1: Create a census
	// Test 1.1: Test with valid data and auth fields
	censusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
			db.OrgMemberAuthFieldsName,
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response to get the census ID
	var createdCensusResponse apicommon.CreateCensusResponse
	err := parseJSON(resp, &createdCensusResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(createdCensusResponse.ID, qt.Not(qt.Equals), "")

	censusID := createdCensusResponse.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Verify the census was created correctly by retrieving it
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var retrievedCensus apicommon.OrganizationCensus
	err = parseJSON(resp, &retrievedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedCensus.ID, qt.Equals, censusID)
	c.Assert(retrievedCensus.Type, qt.Equals, db.CensusTypeSMSorMail)
	c.Assert(retrievedCensus.OrgAddress, qt.Equals, orgAddress)

	// Test 1.2: Test with missing auth fields (should fail)
	censusInfoNoAuth := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		// AuthFields is missing
	}
	_, code = testRequest(t, http.MethodPost, adminToken, censusInfoNoAuth, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test 1.3: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.4: Test with invalid organization address
	invalidCensusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: common.Address{},
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
		},
	}
	_, code = testRequest(t, http.MethodPost, adminToken, invalidCensusInfo, censusEndpoint)
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Get census information
	// Test 2.1: Test with valid census ID (already tested above)

	// Test 2.2: Test with invalid census ID
	_, code = testRequest(t, http.MethodGet, adminToken, nil, censusEndpoint, "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 3: Add members to census
	// Test 3.1: Test with valid data (using the same members we added to the organization)
	censusMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P003",
				Name:         "Carla Johnson",
				Email:        "carla.johnson@example.com",
				Phone:        "+34333333333",
				Password:     "password333",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
			{
				MemberNumber: "P004",
				Name:         "Diego Brown",
				Email:        "diego.brown@example.com",
				Phone:        "+34444444444",
				Password:     "password444",
				Other: map[string]any{
					"department": "HR",
					"age":        42,
				},
			},
		},
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, censusMembers, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains the number of members added
	var addedResponse apicommon.AddMembersResponse
	err = parseJSON(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.Added, qt.Equals, uint32(2))

	// Test 3.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", censusMembers, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 3.3: Test with invalid census ID
	_, code = testRequest(t, http.MethodPost, adminToken, censusMembers, censusEndpoint, "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 3.4: Test with empty members list
	emptyMembersList := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{},
	}
	_, code = testRequest(t, http.MethodPost, adminToken, emptyMembersList, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Test 3.5: Test with async=true flag
	asyncMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P005",
				Name:         "Elsa Smith",
				Email:        "elsa.smith@example.com",
				Phone:        "+34555555555",
				Password:     "password555",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
			{
				MemberNumber: "P006",
				Name:         "Fabian Doe",
				Email:        "fabian.doe@example.com",
				Phone:        "+34666666666",
				Password:     "password666",
				Other: map[string]any{
					"department": "HR",
					"age":        42,
				},
			},
		},
	}

	// Make the request with async=true
	resp, code = testRequest(t, http.MethodPost, adminToken, asyncMembers, censusEndpoint, censusID+"?async=true")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains a job ID
	var asyncResponse apicommon.AddMembersResponse
	err = parseJSON(resp, &asyncResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(asyncResponse.JobID, qt.Not(qt.IsNil))
	c.Assert(len(asyncResponse.JobID), qt.Equals, 16) // JobID should be 16 bytes

	// Convert the job ID to a hex string for the API call
	var jobIDHex internal.HexBytes
	jobIDHex.SetBytes(asyncResponse.JobID)
	t.Logf("Async job ID: %s\n", jobIDHex.String())

	// Check the job progress
	var (
		jobStatus   *db.BulkCensusParticipantStatus
		maxAttempts = 30
		attempts    = 0
		completed   = false
	)

	// Poll the job status until it's complete or max attempts reached
	for attempts < maxAttempts && !completed {
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "census", "job", jobIDHex.String())
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		err = json.Unmarshal(resp, &jobStatus)
		c.Assert(err, qt.IsNil)

		t.Logf("Job progress: %d%%, Added: %d, Total: %d\n",
			jobStatus.Progress, jobStatus.Added, jobStatus.Total)

		if jobStatus.Progress == 100 {
			completed = true
		} else {
			attempts++
			time.Sleep(100 * time.Millisecond) // Wait a bit before checking again
		}
	}

	// Verify the job completed successfully
	c.Assert(completed, qt.Equals, true, qt.Commentf("Job did not complete within expected time"))
	c.Assert(jobStatus.Added, qt.Equals, 2) // We added 2 members
	c.Assert(jobStatus.Total, qt.Equals, 2)
	c.Assert(jobStatus.Progress, qt.Equals, 100)

	// Test 4: Publish census
	// Test 4.1: Test with valid data
	resp, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var publishedCensus apicommon.PublishedCensusResponse
	err = parseJSON(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Test 4.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 4.3: Test with invalid census ID
	_, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, "invalid-id", "publish")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 5: Test group-based census creation
	// First, create a member group
	groupRequest := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "Test Group",
		Description: "A test group for census creation",
		MemberIDs:   []string{}, // We'll need to get member IDs from the organization
	}

	// Get organization members to add to the group
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var orgMembersResponse apicommon.OrganizationMembersResponse
	err = parseJSON(resp, &orgMembersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(orgMembersResponse.Members), qt.Equals, 6)

	// Add member IDs to the group request
	for _, member := range orgMembersResponse.Members {
		groupRequest.MemberIDs = append(groupRequest.MemberIDs, member.ID)
	}

	// Create the group
	resp, code = testRequest(t, http.MethodPost, adminToken, groupRequest, "organizations", orgAddress.String(), "groups")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var createdGroup apicommon.OrganizationMemberGroupInfo
	err = parseJSON(resp, &createdGroup)
	c.Assert(err, qt.IsNil)
	c.Assert(createdGroup.ID, qt.Not(qt.Equals), "")

	// Test 5.1: Create a census based on the group
	groupCensusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		GroupID:    createdGroup.ID,
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
			db.OrgMemberAuthFieldsName,
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, groupCensusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var groupCensusResponse apicommon.CreateCensusResponse
	err = parseJSON(resp, &groupCensusResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(groupCensusResponse.ID, qt.Not(qt.Equals), "")

	groupCensusID := groupCensusResponse.ID
	t.Logf("Created group-based census with ID: %s\n", groupCensusID)

	// Test 5.2: Test with invalid group ID
	invalidGroupCensusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		GroupID:    "invalid-group-id",
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
		},
	}
	_, code = testRequest(t, http.MethodPost, adminToken, invalidGroupCensusInfo, censusEndpoint)
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 6: Test census creation with duplicate auth field values
	// Add members with duplicate email addresses to test validation
	duplicateMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 A",
				Email:        "duplicate7a@example.com",
				Phone:        "+34777777111",
				Password:     "password7a",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 B",
				Email:        "duplicate7b@example.com",
				Phone:        "+34777777222",
				Password:     "password7b",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 C",
				Email:        "duplicate7c@example.com",
				Phone:        "+34777777333",
				Password:     "password7c",
			},
		},
	}

	// Add duplicate members to the organization
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		duplicateMembers,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Create a new group with all members including duplicates
	allMembersResp, code := testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	var allMembersResponse apicommon.OrganizationMembersResponse
	err = parseJSON(allMembersResp, &allMembersResponse)
	c.Assert(err, qt.IsNil)

	var allMemberIDs []string
	for _, member := range allMembersResponse.Members {
		allMemberIDs = append(allMemberIDs, member.ID)
	}

	duplicateGroupRequest := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "Duplicate Test Group",
		Description: "A group with duplicate member numbers",
		MemberIDs:   allMemberIDs,
	}

	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		duplicateGroupRequest,
		"organizations",
		orgAddress.String(),
		"groups",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var duplicateGroup apicommon.OrganizationMemberGroupInfo
	err = parseJSON(resp, &duplicateGroup)
	c.Assert(err, qt.IsNil)

	// Test 6.1: Try to create a census with duplicate member number auth field (should fail)
	duplicateCensusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		GroupID:    duplicateGroup.ID,
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber, // This will have duplicates
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, duplicateCensusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))

	// The response should contain information about the duplicates
	var errorResponse map[string]any
	err = parseJSON(resp, &errorResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(errorResponse["data"], qt.Not(qt.IsNil))

	// to decode data we need to Marshal and Unmarshal
	bytes, err := json.Marshal(errorResponse["data"])
	c.Assert(err, qt.IsNil)

	var aggregationResults db.OrgMemberAggregationResults
	err = json.Unmarshal(bytes, &aggregationResults)
	c.Assert(err, qt.IsNil, qt.Commentf("%#v\n", errorResponse["data"]))
	c.Assert(aggregationResults.Duplicates, qt.HasLen, len(duplicateMembers.Members), qt.Commentf("aggregationResults: %+v", aggregationResults))

	// Test 7: Test census creation with empty auth field values
	// Add a member with empty email to test validation
	emptyFieldMember := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P008",
				Name:         "Empty Email User",
				Email:        "", // Empty email
				Phone:        "+34888888888",
				Password:     "password888",
			},
		},
	}

	// Add member with empty field to the organization
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		emptyFieldMember,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Create a group with the empty field member
	updatedMembersResp, code := testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	var updatedMembersResponse apicommon.OrganizationMembersResponse
	err = parseJSON(updatedMembersResp, &updatedMembersResponse)
	c.Assert(err, qt.IsNil)

	var updatedMemberIDs []string
	for _, member := range updatedMembersResponse.Members {
		updatedMemberIDs = append(updatedMemberIDs, member.ID)
	}

	emptyFieldGroupRequest := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "Empty Field Test Group",
		Description: "A group with empty email field",
		MemberIDs:   updatedMemberIDs,
	}

	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		emptyFieldGroupRequest,
		"organizations",
		orgAddress.String(),
		"groups",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var emptyFieldGroup apicommon.OrganizationMemberGroupInfo
	err = parseJSON(resp, &emptyFieldGroup)
	c.Assert(err, qt.IsNil)

	// Test 7.1: Try to create a census with email twoFa field when some members have empty emails (should fail)
	emptyFieldCensusInfo := &apicommon.CreateCensusRequest{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress,
		GroupID:    emptyFieldGroup.ID,
		TwoFaFields: db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail, // This will have empty values
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, emptyFieldCensusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))

	// The response should contain information about the empty fields
	var emptyErrorResponse map[string]any
	err = parseJSON(resp, &emptyErrorResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(emptyErrorResponse["data"], qt.Not(qt.IsNil))

	// Test 8: Create a user with manager role and test permissions
	// Create a second user
	managerToken := testCreateUser(t, "managerpassword123")

	// Verify the token works
	resp, code = testRequest(t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("Manager user: %s\n", resp)

	// Add the user as a manager to the organization
	// This would require implementing a helper to add a user to an organization with a specific role
	// For now, we'll skip this test as it would require additional API endpoints not covered in this test file
}

// Helper function to parse JSON responses
func parseJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
