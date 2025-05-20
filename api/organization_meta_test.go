package api

import (
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestOrganizationMeta(t *testing.T) {
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

	// Test 1: Add organization meta
	// Test 1.1: Test with valid data
	metaInfo := &apicommon.OrganizationAddMetaRequest{
		Meta: map[string]any{
			"region":   "Europe",
			"size":     "Medium",
			"industry": "Technology",
			"founded":  2020,
			"public":   true,
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, metaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1.2: Test with no authentication
	_, code = testRequest(t, http.MethodPost, "", metaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 1.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodPost, adminToken, metaInfo, "organizations", "invalid-address", "meta")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Get organization meta
	// Test 2.1: Test with valid organization address
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var retrievedMeta apicommon.OrganizationMetaResponse
	err := parseJSON(resp, &retrievedMeta)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedMeta.Meta["region"], qt.Equals, "Europe")
	c.Assert(retrievedMeta.Meta["size"], qt.Equals, "Medium")
	c.Assert(retrievedMeta.Meta["industry"], qt.Equals, "Technology")
	c.Assert(retrievedMeta.Meta["founded"], qt.Equals, float64(2020)) // JSON numbers are parsed as float64
	c.Assert(retrievedMeta.Meta["public"], qt.Equals, true)

	// Test 2.2: Test with no authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 2.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", "invalid-address", "meta")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 3: Update organization meta
	// Test 3.1: Test with valid data - partial update
	updateMetaInfo := &apicommon.OrganizationAddMetaRequest{
		Meta: map[string]any{
			"size":      "Large",
			"employees": 500,
			"locations": []string{"Madrid", "Barcelona", "Valencia"},
		},
	}
	resp, code = testRequest(t, http.MethodPut, adminToken, updateMetaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the update worked by getting the meta again
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	err = parseJSON(resp, &retrievedMeta)
	c.Assert(err, qt.IsNil)

	// Verify updated and new fields
	c.Assert(retrievedMeta.Meta["size"], qt.Equals, "Large")           // Updated field
	c.Assert(retrievedMeta.Meta["employees"], qt.Equals, float64(500)) // New field

	// Test for array values
	locations, ok := retrievedMeta.Meta["locations"].([]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(len(locations), qt.Equals, 3)
	c.Assert(locations[0], qt.Equals, "Madrid")
	c.Assert(locations[1], qt.Equals, "Barcelona")
	c.Assert(locations[2], qt.Equals, "Valencia")

	// IMPORTANT: Verify that all original fields that weren't updated remain intact
	c.Assert(retrievedMeta.Meta["region"], qt.Equals, "Europe")       // Original field preserved
	c.Assert(retrievedMeta.Meta["industry"], qt.Equals, "Technology") // Original field preserved
	c.Assert(retrievedMeta.Meta["founded"], qt.Equals, float64(2020)) // Original field preserved
	c.Assert(retrievedMeta.Meta["public"], qt.Equals, true)           // Original field preserved

	// Count total fields to ensure no fields were lost
	metaFieldCount := 0
	for range retrievedMeta.Meta {
		metaFieldCount++
	}
	c.Assert(metaFieldCount, qt.Equals, 7) // 5 original - 1 updated + 3 new = 7 fields

	// Test 3.2: Test with no authentication
	_, code = testRequest(t, http.MethodPut, "", updateMetaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 3.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodPut, adminToken, updateMetaInfo, "organizations", "invalid-address", "meta")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 4: Delete organization meta fields
	// Test 4.1: Test with valid data
	deleteMetaInfo := &apicommon.OrganizationDeleteMetaRequest{
		Keys: []string{"industry", "founded"},
	}
	resp, code = testRequest(
		t, http.MethodDelete, adminToken, deleteMetaInfo,
		"organizations", orgAddress.String(), "meta",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Wait a moment to ensure the delete operation is processed
	time.Sleep(100 * time.Millisecond)

	// Make a fresh request to get the updated meta
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response into a new variable to ensure we get fresh data
	var updatedMeta apicommon.OrganizationMetaResponse
	err = parseJSON(resp, &updatedMeta)
	c.Assert(err, qt.IsNil)

	// Verify deleted fields are gone
	_, hasIndustry := updatedMeta.Meta["industry"]
	c.Assert(hasIndustry, qt.Equals, false) // Deleted field
	_, hasFounded := updatedMeta.Meta["founded"]
	c.Assert(hasFounded, qt.Equals, false) // Deleted field

	// Verify all other fields remain intact
	c.Assert(updatedMeta.Meta["region"], qt.Equals, "Europe")        // Preserved field
	c.Assert(updatedMeta.Meta["size"], qt.Equals, "Large")           // Preserved field
	c.Assert(updatedMeta.Meta["public"], qt.Equals, true)            // Preserved field
	c.Assert(updatedMeta.Meta["employees"], qt.Equals, float64(500)) // Preserved field

	// Verify locations array is still intact
	locations, ok = updatedMeta.Meta["locations"].([]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(len(locations), qt.Equals, 3)

	// Count total fields to ensure only the specified fields were deleted
	metaFieldCount = 0
	for range updatedMeta.Meta {
		metaFieldCount++
	}
	c.Assert(metaFieldCount, qt.Equals, 5) // 7 previous - 2 deleted

	// Test 4.2: Test with no authentication
	_, code = testRequest(t, http.MethodDelete, "", deleteMetaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test 4.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodDelete, adminToken, deleteMetaInfo, "organizations", "invalid-address", "meta")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 5: Test with complex nested data
	complexMetaInfo := &apicommon.OrganizationAddMetaRequest{
		Meta: map[string]any{
			"contact": map[string]any{
				"email": "info@example.com",
				"phone": "+34123456789",
				"address": map[string]any{
					"street":     "Gran Via",
					"city":       "Madrid",
					"country":    "Spain",
					"postalCode": "28013",
				},
			},
			"socialMedia": map[string]any{
				"twitter":  "example_twitter",
				"linkedin": "example_linkedin",
			},
		},
	}
	resp, code = testRequest(t, http.MethodPost, adminToken, complexMetaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Wait a moment to ensure the POST operation is processed
	time.Sleep(100 * time.Millisecond)

	// Make a fresh request to get the updated meta
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response into a new variable to ensure we get fresh data
	var complexMeta apicommon.OrganizationMetaResponse
	err = parseJSON(resp, &complexMeta)
	c.Assert(err, qt.IsNil)

	// Check nested objects
	contact, ok := complexMeta.Meta["contact"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(contact["email"], qt.Equals, "info@example.com")
	c.Assert(contact["phone"], qt.Equals, "+34123456789")

	address, ok := contact["address"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(address["street"], qt.Equals, "Gran Via")
	c.Assert(address["city"], qt.Equals, "Madrid")
	c.Assert(address["country"], qt.Equals, "Spain")
	c.Assert(address["postalCode"], qt.Equals, "28013")

	socialMedia, ok := complexMeta.Meta["socialMedia"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(socialMedia["twitter"], qt.Equals, "example_twitter")
	c.Assert(socialMedia["linkedin"], qt.Equals, "example_linkedin")

	// Test 5.1: Update nested data and verify other data remains intact
	// Note: We need to include all fields we want to preserve in the update request
	// because the UpdateOrganizationMeta function replaces the entire nested object
	updateNestedMetaInfo := &apicommon.OrganizationAddMetaRequest{
		Meta: map[string]any{
			"contact": map[string]any{
				"email":   "info@example.com", // Preserve original field
				"phone":   "+34987654321",     // Update phone
				"website": "www.example.com",  // Add new field
				"address": map[string]any{ // Preserve nested object
					"street":     "Gran Via",
					"city":       "Madrid",
					"country":    "Spain",
					"postalCode": "28013",
				},
			},
			"socialMedia": map[string]any{
				"twitter":  "example_twitter",  // Preserve original field
				"linkedin": "example_linkedin", // Preserve original field
				"facebook": "example_facebook", // Add new social media
			},
		},
	}
	resp, code = testRequest(
		t, http.MethodPut, adminToken, updateNestedMetaInfo,
		"organizations", orgAddress.String(), "meta",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Wait a moment to ensure the PUT operation is processed
	time.Sleep(100 * time.Millisecond)

	// Make a fresh request to get the updated meta
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Parse the response into a new variable to ensure we get fresh data
	var updatedNestedMeta apicommon.OrganizationMetaResponse
	err = parseJSON(resp, &updatedNestedMeta)
	c.Assert(err, qt.IsNil)

	// Verify updated nested fields
	contact, ok = updatedNestedMeta.Meta["contact"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(contact["phone"], qt.Equals, "+34987654321")      // Updated field
	c.Assert(contact["website"], qt.Equals, "www.example.com") // New field

	// Verify original nested fields remain intact
	c.Assert(contact["email"], qt.Equals, "info@example.com") // Original field preserved

	// Verify nested address object remains intact
	address, ok = contact["address"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(address["street"], qt.Equals, "Gran Via")  // Original field preserved
	c.Assert(address["city"], qt.Equals, "Madrid")      // Original field preserved
	c.Assert(address["country"], qt.Equals, "Spain")    // Original field preserved
	c.Assert(address["postalCode"], qt.Equals, "28013") // Original field preserved

	// Verify social media updates
	socialMedia, ok = updatedNestedMeta.Meta["socialMedia"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(socialMedia["twitter"], qt.Equals, "example_twitter")   // Original field preserved
	c.Assert(socialMedia["linkedin"], qt.Equals, "example_linkedin") // Original field preserved
	c.Assert(socialMedia["facebook"], qt.Equals, "example_facebook") // New field

	// Test 6: Create a user with manager role and test permissions
	// Create a second user
	managerToken := testCreateUser(t, "managerpassword123")

	// Verify the token works
	resp, code = testRequest(t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("Manager user: %s\n", resp)

	// Test 6.1: Test that the manager can't access the organization meta initially
	_, code = testRequest(t, http.MethodGet, managerToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
}
