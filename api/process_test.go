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

	resp, code := testRequest(t, http.MethodPost, adminToken, members, censusEndpoint, censusID.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Publish the census
	resp, code = testRequest(t, http.MethodPost, adminToken, nil, censusEndpoint, censusID.String(), "publish")
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
	censusRoot := publishedCensus.Root
	c.Assert(err, qt.IsNil)

	processInfo := &apicommon.CreateProcessRequest{
		PublishedCensusRoot: censusRoot,
		PublishedCensusURI:  publishedCensus.URI,
		CensusID:            censusID,
		Metadata:            []byte(`{"title":"Test Process","description":"This is a test process"}`),
	}

	resp, code = testRequest(t, http.MethodPost, adminToken, processInfo, "process", processID.String())
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
