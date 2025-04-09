package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

func TestProcess(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
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

	// Create a census
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
	censusID := createdCensus.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Add participants to the census
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

	resp, code = testRequest(t, http.MethodPost, adminToken, participants, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Publish the census
	resp, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var publishedCensus apicommon.PublishedCensusResponse
	err = parseJSON(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Test 1: Create a process
	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID: %s\n", processID.String())

	// Test 1.1: Test with valid data
	censusRoot := publishedCensus.Root
	c.Assert(err, qt.IsNil)

	censusIDBytes := internal.HexBytes{}
	err = censusIDBytes.ParseString(censusID)
	c.Assert(err, qt.IsNil)

	processInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: censusRoot,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusIDBytes,
		Metadata:            []byte(`{"title":"Test Process","description":"This is a test process"}`),
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, processInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", processInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.3: Test with invalid process ID
	_, code = testRequest(t, http.MethodPost, adminToken, processInfo, "process", "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 1.4: Test with missing census root/ID
	invalidProcessInfo := &apicommon.CreateProcessRequest{
		PublishedCensusURI: publishedCensus.URI,
		Metadata:           []byte(`{"title":"Test Process","description":"This is a test process"}`),
	}
	_, code = testRequest(t, http.MethodPost, adminToken, invalidProcessInfo, "process", processID.String())
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 1.5: Test with invalid process ID
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "process", "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
}

// TestDraftProcess tests the draft process functionality
func TestDraftProcess(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Create a census
	censusInfo := &apicommon.OrganizationCensus{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress.String(),
	}
	resp, code := testRequest(t, http.MethodPost, adminToken, censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response to get the census ID
	var createdCensus apicommon.OrganizationCensus
	err := parseJSON(resp, &createdCensus)
	c.Assert(err, qt.IsNil)
	censusID := createdCensus.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Add participants to the census
	participants := &apicommon.AddParticipantsRequest{
		Participants: []apicommon.OrgParticipant{
			{
				ParticipantNo: "P001",
				Name:          "John Doe",
				Email:         "john.doe@example.com",
				Phone:         "+34612345678",
				Password:      "password123",
			},
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, participants, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Publish the census
	resp, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var publishedCensus apicommon.PublishedCensusResponse
	err = parseJSON(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID for draft test: %s\n", processID.String())

	censusIDBytes := internal.HexBytes{}
	err = censusIDBytes.ParseString(censusID)
	c.Assert(err, qt.IsNil)

	// Step 1: Create a process with draft=true
	initialMetadata := []byte(`{"title":"Draft Process","description":"This is a draft process"}`)
	draftProcessInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: publishedCensus.Root,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusIDBytes,
		Metadata:            initialMetadata,
		Draft:               true, // Mark as draft
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, draftProcessInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Log("Successfully created draft process")

	// Verify the process was created and is in draft mode
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var createdProcess db.Process
	err = parseJSON(resp, &createdProcess)
	c.Assert(err, qt.IsNil)
	c.Assert(createdProcess.Draft, qt.Equals, true, qt.Commentf("Process should be in draft mode"))
	t.Log("Verified process is in draft mode")

	// Step 2: Update the process with new metadata and draft=false
	updatedMetadata := []byte(`{"title":"Updated Process","description":"This is no longer a draft process"}`)
	updatedProcessInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: publishedCensus.Root,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusIDBytes,
		Metadata:            updatedMetadata,
		Draft:               false, // No longer a draft
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, updatedProcessInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Log("Successfully updated process and set draft=false")

	// Verify the process was updated and is no longer in draft mode
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var updatedProcess db.Process
	err = parseJSON(resp, &updatedProcess)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedProcess.Draft, qt.Equals, false, qt.Commentf("Process should no longer be in draft mode"))
	c.Assert(string(updatedProcess.Metadata), qt.Equals, string(updatedMetadata), qt.Commentf("Process metadata should be updated"))
	t.Log("Verified process is no longer in draft mode and metadata was updated")

	// Step 3: Try to update the process again, which should fail since it's no longer in draft mode
	finalMetadata := []byte(`{"title":"Final Process","description":"This update should fail"}`)
	finalProcessInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: publishedCensus.Root,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusIDBytes,
		Metadata:            finalMetadata,
		Draft:               true, // Try to set back to draft (should fail)
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, finalProcessInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusConflict, qt.Commentf("Should fail with conflict error, got: %d, response: %s", code, resp))
	t.Log("Successfully verified that updating a non-draft process fails")
}
