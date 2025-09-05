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
	adminUser := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	t.Logf("Admin user: %+v\n", adminUser)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress)

	// Get the organization to verify it exists
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())

	// First, add some organization members to test with
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P001",
				Name:         "Alice Doe",
				Email:        "alice.doe@example.com",
				Phone:        "+34611111111",
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
				Phone:        "+34622222222",
				Password:     "password222",
				Other: map[string]any{
					"department": "Marketing",
					"age":        28,
				},
			},
		},
	}

	// Add members to the organization first
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members")

	// Test 1: Create a census
	// Test 1.1: Test with valid data and auth fields
	censusInfo := &apicommon.CreateCensusRequest{
		OrgAddress: orgAddress,
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
			db.OrgMemberAuthFieldsName,
		},
		TwoFaFields: db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail,
		},
	}
	createdCensusResponse := requestAndParse[apicommon.CreateCensusResponse](t, http.MethodPost, adminToken, censusInfo,
		censusEndpoint)
	c.Assert(createdCensusResponse.ID, qt.Not(qt.Equals), "")

	censusID := createdCensusResponse.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Verify the census was created correctly by retrieving it
	retrievedCensus := requestAndParse[apicommon.OrganizationCensus](t, http.MethodGet, adminToken, nil,
		censusEndpoint, censusID)
	c.Assert(retrievedCensus.ID, qt.Equals, censusID)
	c.Assert(retrievedCensus.Type, qt.Equals, db.CensusTypeMail)
	c.Assert(retrievedCensus.OrgAddress, qt.Equals, orgAddress)

	// Test 1.3: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", censusInfo,
		censusEndpoint)

	// Test 1.4: Test with invalid organization address
	invalidCensusInfo := &apicommon.CreateCensusRequest{
		OrgAddress: common.Address{},
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber,
		},
	}
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, adminToken, invalidCensusInfo,
		censusEndpoint)

	// Test 2: Get census information
	// Test 2.1: Test with valid census ID (already tested above)

	// Test 2.2: Test with invalid census ID
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, adminToken, nil,
		censusEndpoint, "invalid-id")

	// Test 3: Add members to census
	// Test 3.1: Test with valid data (using the same members we added to the organization)
	censusMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P003",
				Name:         "Carla Johnson",
				Email:        "carla.johnson@example.com",
				Phone:        "+34633333333",
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
				Phone:        "+34644444444",
				Password:     "password444",
				Other: map[string]any{
					"department": "HR",
					"age":        42,
				},
			},
		},
	}

	addedResponse := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, adminToken, censusMembers,
		censusEndpoint, censusID)
	c.Assert(addedResponse.Added, qt.Equals, uint32(2))

	// Test 3.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", censusMembers, censusEndpoint, censusID)

	// Test 3.3: Test with invalid census ID
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, censusMembers,
		censusEndpoint, "invalid-id")

	// Test 3.4: Test with empty members list
	emptyMembersList := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{},
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, emptyMembersList, censusEndpoint, censusID)

	// Test 3.5: Test with async=true flag
	asyncMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P005",
				Name:         "Elsa Smith",
				Email:        "elsa.smith@example.com",
				Phone:        "+34655555555",
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

	// Make the request with async=true and verify the response contains a job ID
	asyncResponse := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, adminToken, asyncMembers,
		censusEndpoint, censusID+"?async=true")
	c.Assert(len(asyncResponse.JobID), qt.Equals, 16) // JobID should be 16 bytes

	// Convert the job ID to a hex string for the API call
	var jobIDHex internal.HexBytes
	jobIDHex.SetBytes(asyncResponse.JobID)
	t.Logf("Async job ID: %s\n", jobIDHex.String())

	// Check the job progress
	var (
		jobStatus   db.BulkCensusParticipantStatus
		maxAttempts = 30
	)

	// Poll the job status until it's complete or max attempts reached
	for range maxAttempts {
		jobStatus = requestAndParse[db.BulkCensusParticipantStatus](t, http.MethodGet, adminToken, nil,
			"census", "job", jobIDHex.String())

		t.Logf("Job progress: %d%%, Added: %d, Total: %d\n",
			jobStatus.Progress, jobStatus.Added, jobStatus.Total)

		if jobStatus.Progress == 100 {
			break
		}

		time.Sleep(100 * time.Millisecond) // Wait a bit before checking again
	}

	// Verify the job completed successfully
	c.Assert(jobStatus.Progress, qt.Equals, 100, qt.Commentf("Job did not complete within expected time"))
	c.Assert(jobStatus.Added, qt.Equals, 2) // We added 2 members
	c.Assert(jobStatus.Total, qt.Equals, 2)

	// Test 3.6: Test jobs endpoint - basic functionality
	jobsResponse := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs")
	c.Assert(len(jobsResponse.Jobs), qt.Equals, 1, qt.Commentf("expected 1 job (the census participants job)"))
	c.Assert(jobsResponse.TotalPages, qt.Equals, 1)
	c.Assert(jobsResponse.CurrentPage, qt.Equals, 1)

	// Verify the job details
	job := jobsResponse.Jobs[0]
	c.Assert(job.Type, qt.Equals, db.JobTypeCensusParticipants)
	c.Assert(job.Total, qt.Equals, 2)
	c.Assert(job.Added, qt.Equals, 2)
	c.Assert(job.Completed, qt.Equals, true)
	c.Assert(job.CreatedAt.IsZero(), qt.Equals, false)
	c.Assert(job.CompletedAt.IsZero(), qt.Equals, false)
	c.Assert(job.JobID, qt.Equals, jobIDHex.String())
	t.Logf("Found job: ID=%s, Type=%s, Total=%d, Added=%d, Completed=%t",
		job.JobID, job.Type, job.Total, job.Added, job.Completed)

	// Test 3.7: Test jobs endpoint - pagination and filtering
	// Test with pagination
	jobsResponsePaged := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?page=1&pageSize=1")
	c.Assert(len(jobsResponsePaged.Jobs), qt.Equals, 1)
	c.Assert(jobsResponsePaged.TotalPages, qt.Equals, 1)
	c.Assert(jobsResponsePaged.CurrentPage, qt.Equals, 1)

	// Test with job type filter
	jobsResponseFiltered := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=census_participants")
	c.Assert(len(jobsResponseFiltered.Jobs), qt.Equals, 1)
	c.Assert(jobsResponseFiltered.Jobs[0].Type, qt.Equals, db.JobTypeCensusParticipants)

	// Test with different job type filter (should return empty)
	jobsResponseEmpty := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=org_members")
	c.Assert(len(jobsResponseEmpty.Jobs), qt.Equals, 0, qt.Commentf("should be empty for org_members filter"))

	// Test 3.8: Test jobs endpoint - authorization and error cases
	// Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, "", nil,
		"organizations", orgAddress.String(), "jobs")

	// Test with invalid organization address
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodGet, adminToken, nil,
		"organizations", "invalid-address", "jobs")

	// Test with invalid job type filter
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=invalid_type")

	// Test 4: Publish census
	// Test 4.1: Test with valid data
	publishedCensus := requestAndParse[apicommon.PublishedCensusResponse](t, http.MethodPost, adminToken, nil,
		censusEndpoint, censusID, "publish")
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Test 4.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", nil, censusEndpoint, censusID, "publish")

	// Test 4.3: Test with invalid census ID
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, nil, censusEndpoint, "invalid-id", "publish")

	// Test 5: Test with manager user permissions
	// Add members with duplicate member numbers to test validation
	duplicateMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 A",
				Email:        "duplicate7a@example.com",
				Phone:        "+34677777111",
				Password:     "password7a",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 B",
				Email:        "duplicate7b@example.com",
				Phone:        "+34677777222",
				Password:     "password7b",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 C",
				Email:        "duplicate7c@example.com",
				Phone:        "+34677777333",
				Password:     "password7c",
			},
		},
	}

	// Add duplicate members to the organization
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, duplicateMembers,
		"organizations", orgAddress.String(), "members")

	// Fetch updated organization members (needed for the server-side validation)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")

	// Test 6.1: Create a census with members having duplicate member numbers
	// Note: After simplification, duplicate validation has been removed from the handler
	duplicateCensusInfo := &apicommon.CreateCensusRequest{
		OrgAddress: orgAddress,
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber, // Has duplicates, but now accepted
		},
	}
	duplicateCensusResponse := requestAndParse[apicommon.CreateCensusResponse](
		t, http.MethodPost, adminToken, duplicateCensusInfo, censusEndpoint)
	c.Assert(duplicateCensusResponse.ID, qt.Not(qt.Equals), "")

	// Test 7: Test census creation with empty auth field values
	// Add a member with empty email to test validation
	emptyFieldMember := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P008",
				Name:         "Empty Email User",
				Email:        "", // Empty email
				Phone:        "+34688888888",
				Password:     "password888",
			},
		},
	}

	// Add member with empty field to the organization
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, emptyFieldMember,
		"organizations", orgAddress.String(), "members")

	// Fetch updated organization members (needed for the server-side validation)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")

	// Test 7.1: Create a census with email twoFa field when some members have empty emails
	// Note: After simplification, empty field validation has been removed from the handler
	emptyFieldCensusInfo := &apicommon.CreateCensusRequest{
		OrgAddress: orgAddress,
		TwoFaFields: db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail, // Has empty values, but now accepted
		},
	}
	emptyFieldCensusResponse := requestAndParse[apicommon.CreateCensusResponse](
		t, http.MethodPost, adminToken, emptyFieldCensusInfo, censusEndpoint)
	c.Assert(emptyFieldCensusResponse.ID, qt.Not(qt.Equals), "")

	// Test 8: Create a user with manager role and test permissions
	// Create a second user
	managerToken := testCreateUser(t, "managerpassword123")

	managerUser := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	t.Logf("Manager user: %+v\n", managerUser)

	// Add the user as a manager to the organization
	// This would require implementing a helper to add a user to an organization with a specific role
	// For now, we'll skip this test as it would require additional API endpoints not covered in this test file

	// Test 9: Publish Group Census
	t.Run("PublishGroupCensus", func(t *testing.T) {
		c := qt.New(t)

		// Create a group with the existing members
		// Get the members to get their IDs
		orgMembersResp := requestAndParse[apicommon.OrganizationMembersResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "members")

		// Take the first two members for our group
		c.Assert(len(orgMembersResp.Members) >= 2, qt.IsTrue,
			qt.Commentf("Not enough members for testing, need at least 2"))

		memberIDs := []string{
			orgMembersResp.Members[0].ID,
			orgMembersResp.Members[1].ID,
		}

		// Create the group
		createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
			Title:       "Test Census Group",
			Description: "Group for testing census publishing",
			MemberIDs:   memberIDs,
		}

		groupResp := requestAndParse[apicommon.OrganizationMemberGroupInfo](
			t, http.MethodPost, adminToken, createGroupReq,
			"organizations", orgAddress.String(), "groups")

		c.Assert(groupResp.ID, qt.Not(qt.Equals), "")
		groupID := groupResp.ID
		t.Logf("Created member group with ID: %s", groupID)

		// Create a new empty census
		groupCensusInfo := &apicommon.CreateCensusRequest{
			OrgAddress: orgAddress,
			AuthFields: db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsMemberNumber,
				db.OrgMemberAuthFieldsName,
			},
			TwoFaFields: db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
			},
		}

		groupCensusResp := requestAndParse[apicommon.CreateCensusResponse](
			t, http.MethodPost, adminToken, groupCensusInfo, censusEndpoint)
		c.Assert(groupCensusResp.ID, qt.Not(qt.Equals), "")
		groupCensusID := groupCensusResp.ID
		t.Logf("Created group census with ID: %s", groupCensusID)

		// Create the request body with authentication and two-factor fields
		publishGroupRequest := &apicommon.PublishCensusGroupRequest{
			AuthFields: db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsMemberNumber,
				db.OrgMemberAuthFieldsName,
			},
			TwoFaFields: db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
			},
		}

		// Test 9.1: Successful group census publication
		publishedGroupCensus := requestAndParse[apicommon.PublishedCensusResponse](
			t, http.MethodPost, adminToken, publishGroupRequest,
			censusEndpoint, groupCensusID, "group", groupID, "publish")

		c.Assert(publishedGroupCensus.URI, qt.Not(qt.Equals), "")
		c.Assert(publishedGroupCensus.Root, qt.Not(qt.Equals), "")
		c.Assert(publishedGroupCensus.Size, qt.Equals, int64(2)) // 2 members in the group
		t.Logf("Published group census with URI: %s and Root: %s",
			publishedGroupCensus.URI, publishedGroupCensus.Root)

		// Verify that the census participants are correctly set
		participantsResp := requestAndParse[apicommon.CensusParticipantsResponse](
			t, http.MethodGet, adminToken, nil,
			censusEndpoint, groupCensusID, "participants")
		c.Assert(len(participantsResp.MemberIDs), qt.Equals, 2)
		c.Assert(participantsResp.MemberIDs[0], qt.Equals, memberIDs[0])
		c.Assert(participantsResp.MemberIDs[1], qt.Equals, memberIDs[1])

		// Test 9.2: Test with already published census
		// Publishing again should return the same information
		publishedAgain := requestAndParse[apicommon.PublishedCensusResponse](
			t, http.MethodPost, adminToken, publishGroupRequest,
			censusEndpoint, groupCensusID, "group", groupID, "publish")

		c.Assert(publishedAgain.URI, qt.Equals, publishedGroupCensus.URI)
		c.Assert(publishedAgain.Root.String(), qt.Equals, publishedGroupCensus.Root.String())

		// Test 9.3: Test with no authentication
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPost, "", publishGroupRequest,
			censusEndpoint, groupCensusID, "group", groupID, "publish")

		// Test 9.4: Test with invalid census ID
		requestAndAssertCode(http.StatusBadRequest,
			t, http.MethodPost, adminToken, publishGroupRequest,
			censusEndpoint, "invalid-id", "group", groupID, "publish")

		// Test 9.5: Test with invalid group ID
		requestAndAssertCode(http.StatusBadRequest,
			t, http.MethodPost, adminToken, publishGroupRequest,
			censusEndpoint, groupCensusID, "group", "invalid-id", "publish")

		// Test 9.6: Test with non-existent census
		nonExistentCensusID := "000000000000000000000000" // Valid format but doesn't exist
		requestAndAssertCode(http.StatusNotFound,
			t, http.MethodPost, adminToken, publishGroupRequest,
			censusEndpoint, nonExistentCensusID, "group", groupID, "publish")

		// Test 9.7: Test with non-admin user
		// Create a third user who isn't admin of the organization
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		// Non-admin should not be able to publish group census
		requestAndAssertCode(http.StatusUnauthorized,
			t, http.MethodPost, nonAdminToken, publishGroupRequest,
			censusEndpoint, groupCensusID, "group", groupID, "publish")
	})
}

// Helper function to parse JSON responses
func parseJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func decodeNestedFieldAs[T any](c *qt.C, parsedJSON map[string]any, field string) T {
	c.Assert(parsedJSON[field], qt.Not(qt.IsNil), qt.Commentf("no field %q in json %#v\n", parsedJSON))

	// to decode field we need to Marshal and Unmarshal
	nestedFieldBytes, err := json.Marshal(parsedJSON[field])
	c.Assert(err, qt.IsNil)

	var nestedField T
	err = json.Unmarshal(nestedFieldBytes, &nestedField)
	c.Assert(err, qt.IsNil, qt.Commentf("%#v\n", parsedJSON[field]))
	return nestedField
}
