package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

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

	// Test 1: Create a census
	// Test 1.1: Test with valid data
	censusInfo := &apicommon.OrganizationCensus{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress.String(),
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response to get the census ID
	var createdCensus apicommon.OrganizationCensus
	err := parseJSON(resp, &createdCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(createdCensus.ID, qt.Not(qt.Equals), "")
	c.Assert(createdCensus.Type, qt.Equals, db.CensusTypeSMSorMail)
	c.Assert(createdCensus.OrgAddress, qt.Equals, orgAddress.String())

	censusID := createdCensus.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Test 1.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.3: Test with invalid organization address
	invalidCensusInfo := &apicommon.OrganizationCensus{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: "invalid-address",
	}
	_, code = testRequest(t, http.MethodPost, adminToken, invalidCensusInfo, censusEndpoint)
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Get census information
	// Test 2.1: Test with valid census ID
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var retrievedCensus apicommon.OrganizationCensus
	err = parseJSON(resp, &retrievedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedCensus.ID, qt.Equals, censusID)
	c.Assert(retrievedCensus.Type, qt.Equals, db.CensusTypeSMSorMail)
	c.Assert(retrievedCensus.OrgAddress, qt.Equals, orgAddress.String())

	// Test 2.2: Test with invalid census ID
	_, code = testRequest(t, http.MethodGet, adminToken, nil, censusEndpoint, "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 3: Add members to census
	// Test 3.1: Test with valid data
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

	resp, code = testRequest(t, http.MethodPost, adminToken, members, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains the number of members added
	var addedResponse apicommon.AddMembersResponse
	err = parseJSON(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.Count, qt.Equals, uint32(2))

	// Test 3.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", members, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 3.3: Test with invalid census ID
	_, code = testRequest(t, http.MethodPost, adminToken, members, censusEndpoint, "invalid-id")
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
			{
				MemberNumber: "P004",
				Name:         "Alice Brown",
				Email:        "alice.brown@example.com",
				Phone:        "+34655443322",
				Password:     "passwordabc",
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

	// Test 5: Create a user with manager role and test permissions
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
