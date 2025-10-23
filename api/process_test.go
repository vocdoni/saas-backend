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

	// Test 1: Create a process
	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID: %s\n", processID.String())

	// Test 1.1: Test with valid data
	censusIDBytes := internal.HexBytes{}
	err = censusIDBytes.ParseString(censusID)
	c.Assert(err, qt.IsNil)

	processInfo := &apicommon.CreateProcessRequest{
		CensusID: censusIDBytes,
		Metadata: map[string]any{"title": "Test Process", "description": "This is a test process"},
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, processInfo, "process")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1.2: Test create with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", processInfo, "process")

	// Test 1.3: Test create with missing census ID
	invalidProcessInfo := &apicommon.CreateProcessRequest{
		Metadata: map[string]any{"title": "Test Process", "description": "This is a test process"},
	}

	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, invalidProcessInfo, "process")

	// Test 1.4: Test create with invalid census ID
	invalidCensusID := internal.HexBytes("invalid-id")
	invalidProcessInfo2 := &apicommon.CreateProcessRequest{
		CensusID: invalidCensusID,
		Metadata: map[string]any{"title": "Test Process", "description": "This is a test process"},
	}

	_, code = testRequest(t, http.MethodPost, adminToken, invalidProcessInfo2, "process")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 1.5: Test create process (should succeed)
	pid := requestAndParseWithAssertCode[string](http.StatusOK, t, http.MethodPost, adminToken, processInfo, "process")
	t.Logf("Created process with ID: %s\n", pid)

	// Test 1.6: Test retrieve with invalid census ID
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "process", "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 1.7: Test retrieve with valid process ID
	retrievedProcess := requestAndParse[db.Process](t, http.MethodGet, adminToken, nil, "process", pid)
	c.Assert(retrievedProcess.ID.Hex(), qt.Equals, pid)
	c.Assert(retrievedProcess.Metadata["title"], qt.Equals, "Test Process")
	c.Assert(retrievedProcess.Metadata["description"], qt.Equals, "This is a test process")
}
