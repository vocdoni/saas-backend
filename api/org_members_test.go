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

func TestOrganizationMembers(t *testing.T) {
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
	c.Assert(
		code,
		qt.Equals,
		http.StatusOK,
		qt.Commentf("response: %s", resp),
	)

	// Test 1: Get organization members (initially empty)
	// Test 1.1: Test with valid organization address
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var membersResponse apicommon.OrganizationMembersResponse
	err := json.Unmarshal(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 0, qt.Commentf("expected empty members list"))

	// Test 1.2: Test with no authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", "invalid-address", "members")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Add members to organization
	// Test 2.1: Test with valid data
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberID: "P001",
				Name:     "John Doe",
				Email:    "john.doe@example.com",
				Phone:    "+34612345678",
				Password: "password123",
				Other: map[string]any{
					"department": "Engineering",
					"age":        30,
				},
			},
			{
				MemberID: "P002",
				Name:     "Jane Smith",
				Email:    "jane.smith@example.com",
				Phone:    "+34698765432",
				Password: "password456",
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

	// Verify the response contains the number of members added
	var addedResponse apicommon.AddMembersResponse
	err = json.Unmarshal(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.Count, qt.Equals, uint32(2))

	// Test 2.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", members, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 2.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodPost, adminToken, members, "organizations", "invalid-address", "members")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2.4: Test with empty members list
	emptyMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{},
	}
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		emptyMembers,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response for empty members list
	err = json.Unmarshal(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.Count, qt.Equals, uint32(0))

	// Test 3: Get organization members (now with added members)
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 2, qt.Commentf("expected 2 members"))

	// Test 4: Add members asynchronously
	// Test 4.1: Test with valid data and async=true
	asyncMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberID: "P003",
				Name:     "Bob Johnson",
				Email:    "bob.johnson@example.com",
				Phone:    "+34611223344",
				Password: "password789",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
			{
				MemberID: "P004",
				Name:     "Alice Brown",
				Email:    "alice.brown@example.com",
				Phone:    "+34655443322",
				Password: "passwordabc",
				Other: map[string]any{
					"department": "HR",
					"age":        42,
				},
			},
		},
	}

	// Make the request with async=true
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		asyncMembers,
		"organizations",
		orgAddress.String(),
		"members?async=true",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains a job ID
	var asyncResponse apicommon.AddMembersResponse
	err = json.Unmarshal(resp, &asyncResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(asyncResponse.JobID, qt.Not(qt.IsNil))
	c.Assert(len(asyncResponse.JobID), qt.Equals, 16) // JobID should be 16 bytes

	// Convert the job ID to a hex string for the API call
	var jobIDHex internal.HexBytes
	jobIDHex.SetBytes(asyncResponse.JobID)
	t.Logf("Async job ID: %s\n", jobIDHex.String())

	// Test 5: Check the job progress
	var (
		jobStatus   *db.BulkOrgMembersStatus
		maxAttempts = 30
		attempts    = 0
		completed   = false
	)

	// Poll the job status until it's complete or max attempts reached
	for attempts < maxAttempts && !completed {
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"members",
			"job",
			jobIDHex.String(),
		)
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

	// Test 6: Get organization members with pagination
	// Test 6.1: Test with page=1 and pageSize=2
	resp, code = testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"members?page=1&pageSize=2",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 2, qt.Commentf("expected 2 members with pagination"))

	// Test 6.2: Test with page=2 and pageSize=2
	resp, code = testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"members?page=2&pageSize=2",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(
		len(membersResponse.Members),
		qt.Equals,
		2,
		qt.Commentf("expected 2 members on page 2"),
	)

	// Test 7: Delete members
	// Test 7.1: Test with valid member IDs
	deleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{
			membersResponse.Members[0].ID,
			membersResponse.Members[1].ID,
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
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains the number of members deleted
	var deleteResponse apicommon.DeleteMembersResponse
	err = json.Unmarshal(resp, &deleteResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(deleteResponse.Count, qt.Equals, 2, qt.Commentf("expected 2 members deleted"))

	// Test 7.2: Test with no authentication
	_, code = testRequest(t, http.MethodDelete, "", deleteRequest, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 7.3: Test with invalid organization address
	_, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		deleteRequest,
		"organizations",
		"invalid-address",
		"members",
	)
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 7.4: Test with empty member IDs list
	emptyDeleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{},
	}
	resp, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		emptyDeleteRequest,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response for empty member IDs list
	err = json.Unmarshal(resp, &deleteResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(deleteResponse.Count, qt.Equals, 0, qt.Commentf("expected 0 members deleted"))

	// Test 8: Verify members were deleted by getting the list again
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &membersResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersResponse.Members), qt.Equals, 2, qt.Commentf("expected 2 members remaining"))
}
