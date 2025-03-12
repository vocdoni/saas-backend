package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
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
	t.Logf("Created organization with address: %s\n", orgAddress)

	// Get the organization to verify it exists
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Create a census
	censusInfo := &OrganizationCensus{
		Type:       db.CensusTypeSMSorMail,
		OrgAddress: orgAddress.String(),
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, censusInfo, censusEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response to get the census ID
	var createdCensus OrganizationCensus
	err := parseJSON(resp, &createdCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(createdCensus.ID, qt.Not(qt.Equals), "")
	censusID := createdCensus.ID
	t.Logf("Created census with ID: %s\n", censusID)

	// Add participants to the census
	participants := &AddParticipantsRequest{
		Participants: []OrgParticipant{
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

	var publishedCensus PublishedCensusResponse
	err = parseJSON(resp, &publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Test 1: Create a process
	// Generate a random process ID
	processID := internal.HexBytes(util.RandomBytes(32))
	t.Logf("Generated process ID: %s\n", processID.String())

	// Test 1.1: Test with valid data
	censusRoot := internal.HexBytes{}
	err = censusRoot.FromString(publishedCensus.Root)
	c.Assert(err, qt.IsNil)

	censusIDBytes := internal.HexBytes{}
	err = censusIDBytes.FromString(censusID)
	c.Assert(err, qt.IsNil)

	processInfo := &CreateProcessRequest{
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
	invalidProcessInfo := &CreateProcessRequest{
		PublishedCensusURI: publishedCensus.URI,
		Metadata:           []byte(`{"title":"Test Process","description":"This is a test process"}`),
	}
	_, code = testRequest(t, http.MethodPost, adminToken, invalidProcessInfo, "process", processID.String())
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 1.5: Test with invalid process ID
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "process", "invalid-id")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
}
