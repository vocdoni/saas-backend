package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

func testCreateOrgAndCensus(t *testing.T, adminToken string) (common.Address, string, apicommon.PublishedCensusResponse) {
	c := qt.New(t)

	// Verify the token works
	user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	t.Logf("Admin user: %+v\n", user)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Get the organization to verify it exists
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())

	// Create a census
	authFields := db.OrgMemberAuthFields{}
	// use the email for two-factor authentication
	twoFaFields := db.OrgMemberTwoFaFields{
		db.OrgMemberTwoFaFieldEmail,
	}

	censusID := testCreateCensus(t, adminToken, orgAddress, authFields, twoFaFields)
	t.Logf("Created census with ID: %s\n", censusID)

	// Add members to the census
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

	resp, code := testRequest(t, http.MethodPost, adminToken, members, censusEndpoint, censusID)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Publish the census
	resp, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, censusID, "publish")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var publishedCensus apicommon.PublishedCensusResponse
	err := parseJSON(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	return orgAddress, censusID, publishedCensus
}

func TestProcess(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Create org and census
	_, censusID, publishedCensus := testCreateOrgAndCensus(t, adminToken)

	// Test 1: Create a process
	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID: %s\n", processID.String())

	// Test 1.1: Test with valid data
	censusRoot := publishedCensus.Root

	censusIDBytes := internal.HexBytes{}
	err := censusIDBytes.ParseString(censusID)
	c.Assert(err, qt.IsNil)

	processInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: censusRoot,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusIDBytes,
		Metadata:            []byte(`{"title":"Test Process","description":"This is a test process"}`),
	}

	resp, code := testRequest(t, http.MethodPost, adminToken, processInfo, "process", processID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", processInfo, "process", processID.String())

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

	// Create org and census
	orgAddress, censusID, publishedCensus := testCreateOrgAndCensus(t, adminToken)

	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID for draft test: %s\n", processID.String())

	censusIDBytes := internal.HexBytes{}
	err := censusIDBytes.ParseString(censusID)
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

	resp, code := testRequest(t, http.MethodPost, adminToken, draftProcessInfo, "process", processID.String())
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

	// Verify the list of draft processes contains 1 item
	{
		processDraftsResp := requestAndParse[apicommon.ListOrganizationProcesses](t,
			http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "processes", "drafts")
		c.Assert(processDraftsResp.Processes, qt.HasLen, 1)
	}

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

	// Verify the list of draft processes is now empty
	{
		processDraftsResp := requestAndParse[apicommon.ListOrganizationProcesses](t,
			http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "processes", "drafts")
		c.Assert(processDraftsResp.Processes, qt.HasLen, 0)
		c.Assert(processDraftsResp.TotalPages, qt.Equals, 0)
	}

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
