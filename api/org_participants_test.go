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

func TestOrganizationParticipants(t *testing.T) {
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

	// Test 1: Get organization participants (initially empty)
	// Test 1.1: Test with valid organization address
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var participantsResponse apicommon.OrganizationParticipantsResponse
	err := json.Unmarshal(resp, &participantsResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(participantsResponse.Participants), qt.Equals, 0, qt.Commentf("expected empty participants list"))

	// Test 1.2: Test with no authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", "invalid-address", "participants")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Add participants to organization
	// Test 2.1: Test with valid data
	participants := &apicommon.AddParticipantsRequest{
		Participants: []apicommon.OrgParticipant{
			{
				ParticipantNo: "P001",
				Name:          "John Doe",
				Email:         "john.doe@example.com",
				Phone:         "+34612345678",
				Password:      "password123",
				Other: map[string]any{
					"department": "Engineering",
					"age":        30,
				},
			},
			{
				ParticipantNo: "P002",
				Name:          "Jane Smith",
				Email:         "jane.smith@example.com",
				Phone:         "+34698765432",
				Password:      "password456",
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
		participants,
		"organizations",
		orgAddress.String(),
		"participants",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains the number of participants added
	var addedResponse apicommon.AddParticipantsResponse
	err = json.Unmarshal(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.ParticipantsNo, qt.Equals, uint32(2))

	// Test 2.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", participants, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 2.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodPost, adminToken, participants, "organizations", "invalid-address", "participants")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2.4: Test with empty participants list
	emptyParticipants := &apicommon.AddParticipantsRequest{
		Participants: []apicommon.OrgParticipant{},
	}
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		emptyParticipants,
		"organizations",
		orgAddress.String(),
		"participants",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response for empty participants list
	err = json.Unmarshal(resp, &addedResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(addedResponse.ParticipantsNo, qt.Equals, uint32(0))

	// Test 3: Get organization participants (now with added participants)
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &participantsResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(participantsResponse.Participants), qt.Equals, 2, qt.Commentf("expected 2 participants"))

	// Test 4: Add participants asynchronously
	// Test 4.1: Test with valid data and async=true
	asyncParticipants := &apicommon.AddParticipantsRequest{
		Participants: []apicommon.OrgParticipant{
			{
				ParticipantNo: "P003",
				Name:          "Bob Johnson",
				Email:         "bob.johnson@example.com",
				Phone:         "+34611223344",
				Password:      "password789",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
			{
				ParticipantNo: "P004",
				Name:          "Alice Brown",
				Email:         "alice.brown@example.com",
				Phone:         "+34655443322",
				Password:      "passwordabc",
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
		asyncParticipants,
		"organizations",
		orgAddress.String(),
		"participants?async=true",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains a job ID
	var asyncResponse apicommon.AddParticipantsResponse
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
		jobStatus   *db.BulkOrgParticipantsStatus
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
			"participants",
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
	c.Assert(jobStatus.Added, qt.Equals, 2) // We added 2 participants
	c.Assert(jobStatus.Total, qt.Equals, 2)
	c.Assert(jobStatus.Progress, qt.Equals, 100)

	// Test 6: Get organization participants with pagination
	// Test 6.1: Test with page=1 and pageSize=2
	resp, code = testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"participants?page=1&pageSize=2",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &participantsResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(participantsResponse.Participants), qt.Equals, 2, qt.Commentf("expected 2 participants with pagination"))

	// Test 6.2: Test with page=2 and pageSize=2
	resp, code = testRequest(
		t,
		http.MethodGet,
		adminToken,
		nil,
		"organizations",
		orgAddress.String(),
		"participants?page=2&pageSize=2",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &participantsResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(
		len(participantsResponse.Participants),
		qt.Equals,
		2,
		qt.Commentf("expected 2 participants on page 2"),
	)

	// Test 7: Delete participants
	// Test 7.1: Test with valid participant IDs
	deleteRequest := &apicommon.DeleteParticipantsRequest{
		ParticipantIDs: []string{"P001", "P003"},
	}

	resp, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		deleteRequest,
		"organizations",
		orgAddress.String(),
		"participants",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the response contains the number of participants deleted
	var deleteResponse apicommon.DeleteParticipantsResponse
	err = json.Unmarshal(resp, &deleteResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(deleteResponse.ParticipantsNo, qt.Equals, 2, qt.Commentf("expected 2 participants deleted"))

	// Test 7.2: Test with no authentication
	_, code = testRequest(t, http.MethodDelete, "", deleteRequest, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 7.3: Test with invalid organization address
	_, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		deleteRequest,
		"organizations",
		"invalid-address",
		"participants",
	)
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 7.4: Test with empty participant IDs list
	emptyDeleteRequest := &apicommon.DeleteParticipantsRequest{
		ParticipantIDs: []string{},
	}
	resp, code = testRequest(
		t,
		http.MethodDelete,
		adminToken,
		emptyDeleteRequest,
		"organizations",
		orgAddress.String(),
		"participants",
	)
	c.Assert(code, qt.Equals, http.StatusOK)

	// Verify the response for empty participant IDs list
	err = json.Unmarshal(resp, &deleteResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(deleteResponse.ParticipantsNo, qt.Equals, 0, qt.Commentf("expected 0 participants deleted"))

	// Test 8: Verify participants were deleted by getting the list again
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "participants")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = json.Unmarshal(resp, &participantsResponse)
	c.Assert(err, qt.IsNil)
	c.Assert(len(participantsResponse.Participants), qt.Equals, 2, qt.Commentf("expected 2 participants remaining"))
}
