package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

func TestOrganizationMeta(t *testing.T) {
	c := qt.New(t)

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	t.Logf("Admin user: %+v\n", user)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

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
	resp, code := testRequest(t, http.MethodPost, adminToken, metaInfo, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test 1.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", metaInfo, "organizations", orgAddress.String(), "meta")

	// Test 1.3: Test with invalid organization address
	_, code = testRequest(t, http.MethodPost, adminToken, metaInfo, "organizations", "invalid-address", "meta")
	c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

	// Test 2: Get organization meta
	// Test 2.1: Test with valid organization address
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var retrievedMeta apicommon.OrganizationMetaResponse
	c.Assert(json.Unmarshal(resp, &retrievedMeta), qt.IsNil)
	c.Assert(retrievedMeta.Meta["region"], qt.Equals, "Europe")
	c.Assert(retrievedMeta.Meta["size"], qt.Equals, "Medium")
	c.Assert(retrievedMeta.Meta["industry"], qt.Equals, "Technology")
	c.Assert(retrievedMeta.Meta["founded"], qt.Equals, float64(2020)) // JSON numbers are parsed as float64
	c.Assert(retrievedMeta.Meta["public"], qt.IsTrue)

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

	c.Assert(json.Unmarshal(resp, &retrievedMeta), qt.IsNil)

	// Verify updated and new fields
	c.Assert(retrievedMeta.Meta["size"], qt.Equals, "Large")           // Updated field
	c.Assert(retrievedMeta.Meta["employees"], qt.Equals, float64(500)) // New field

	// Test for array values
	locations, ok := retrievedMeta.Meta["locations"].([]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(locations, qt.HasLen, 3)
	c.Assert(locations[0], qt.Equals, "Madrid")
	c.Assert(locations[1], qt.Equals, "Barcelona")
	c.Assert(locations[2], qt.Equals, "Valencia")

	// IMPORTANT: Verify that all original fields that weren't updated remain intact
	c.Assert(retrievedMeta.Meta["region"], qt.Equals, "Europe")       // Original field preserved
	c.Assert(retrievedMeta.Meta["industry"], qt.Equals, "Technology") // Original field preserved
	c.Assert(retrievedMeta.Meta["founded"], qt.Equals, float64(2020)) // Original field preserved
	c.Assert(retrievedMeta.Meta["public"], qt.IsTrue)                 // Original field preserved

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
	c.Assert(json.Unmarshal(resp, &updatedMeta), qt.IsNil)

	// Verify deleted fields are gone
	_, hasIndustry := updatedMeta.Meta["industry"]
	c.Assert(hasIndustry, qt.IsFalse) // Deleted field
	_, hasFounded := updatedMeta.Meta["founded"]
	c.Assert(hasFounded, qt.IsFalse) // Deleted field

	// Verify all other fields remain intact
	c.Assert(updatedMeta.Meta["region"], qt.Equals, "Europe")        // Preserved field
	c.Assert(updatedMeta.Meta["size"], qt.Equals, "Large")           // Preserved field
	c.Assert(updatedMeta.Meta["public"], qt.IsTrue)                  // Preserved field
	c.Assert(updatedMeta.Meta["employees"], qt.Equals, float64(500)) // Preserved field

	// Verify locations array is still intact
	locations, ok = updatedMeta.Meta["locations"].([]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(locations, qt.HasLen, 3)

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
				"phone": "+34600456789",
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
	c.Assert(json.Unmarshal(resp, &complexMeta), qt.IsNil)

	// Check nested objects
	contact, ok := complexMeta.Meta["contact"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(contact["email"], qt.Equals, "info@example.com")
	c.Assert(contact["phone"], qt.Equals, "+34600456789")

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
				"phone":   "+34600654321",     // Update phone
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
	c.Assert(json.Unmarshal(resp, &updatedNestedMeta), qt.IsNil)

	// Verify updated nested fields
	contact, ok = updatedNestedMeta.Meta["contact"].(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(contact["phone"], qt.Equals, "+34600654321")      // Updated field
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
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("Manager user: %s\n", resp)

	// Test 6.1: Test that the manager can't access the organization meta initially
	_, code = testRequest(t, http.MethodGet, managerToken, nil, "organizations", orgAddress.String(), "meta")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
}

// TestOrganizationMetaIdentityKeysAdminOnly checks that managers can write the organization meta
// (as the web app does for its tutorial flags) but cannot change the identity keys (name, logo,
// description) that PUT /organizations already restricts to admins.
func TestOrganizationMetaIdentityKeysAdminOnly(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}

	// give the organization an identity through the admin-only PUT /organizations
	orgUpdate := &apicommon.OrganizationInfo{
		Name: &apicommon.MultilingualText{"default": "Acme", "es": "Acme ES"},
		Logo: &apicommon.MultilingualText{"default": "https://example.com/logo.png"},
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, orgUpdate, "organizations", orgAddress.String())

	// make a second user a manager of the organization
	managerToken := testCreateUser(t, "managerpassword123")
	managerInfo := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	managerUser, err := testDB.UserByEmail(managerInfo.Email)
	c.Assert(err, qt.IsNil)
	managerUser.Organizations = append(managerUser.Organizations, db.OrganizationUser{
		Address: orgAddress,
		Role:    db.ManagerRole,
	})
	_, err = testDB.SetUser(managerUser)
	c.Assert(err, qt.IsNil)

	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}
	original := getMeta()
	c.Assert(original["name"], qt.IsNotNil)
	c.Assert(original["logo"], qt.IsNotNil)

	t.Run("manager can read meta", func(t *testing.T) {
		requestAndAssertCode(http.StatusOK, t, http.MethodGet, managerToken, nil, metaPath...)
	})

	t.Run("manager can write non-identity keys", func(t *testing.T) {
		c := qt.New(t)
		// mirror the web app: PUT back the whole meta map it read, plus a tutorial flag
		meta := requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, managerToken, nil, metaPath...).Meta
		meta["isSidebarTutorialClosed"] = true
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: meta}, metaPath...)
		c.Assert(getMeta()["isSidebarTutorialClosed"], qt.IsTrue)

		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, managerToken,
			&apicommon.OrganizationDeleteMetaRequest{Keys: []string{"isSidebarTutorialClosed"}}, metaPath...)
		_, ok := getMeta()["isSidebarTutorialClosed"]
		c.Assert(ok, qt.IsFalse)

		// POST replacing the whole map is fine as long as the identity keys are kept as stored
		replace := getMeta()
		replace["region"] = "Europe"
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: replace}, metaPath...)
		c.Assert(getMeta()["region"], qt.Equals, "Europe")
	})

	t.Run("manager cannot change identity keys", func(t *testing.T) {
		c := qt.New(t)
		for _, tc := range []struct {
			name   string
			method string
			body   any
		}{
			{"put name", http.MethodPut, &apicommon.OrganizationAddMetaRequest{
				Meta: map[string]any{"name": map[string]any{"default": "Evil Corp"}},
			}},
			{"put dotted name", http.MethodPut, &apicommon.OrganizationAddMetaRequest{
				Meta: map[string]any{"name.default": "Evil Corp"},
			}},
			{"put new description", http.MethodPut, &apicommon.OrganizationAddMetaRequest{
				Meta: map[string]any{"description": map[string]any{"default": "pay here"}},
			}},
			{"post replacing logo", http.MethodPost, &apicommon.OrganizationAddMetaRequest{
				Meta: map[string]any{"name": original["name"], "logo": map[string]any{"default": "https://evil.example/x.png"}},
			}},
			{"post dropping identity", http.MethodPost, &apicommon.OrganizationAddMetaRequest{
				Meta: map[string]any{"region": "Europe"},
			}},
			{"delete logo", http.MethodDelete, &apicommon.OrganizationDeleteMetaRequest{Keys: []string{"logo"}}},
			{"delete dotted name", http.MethodDelete, &apicommon.OrganizationDeleteMetaRequest{Keys: []string{"name.es"}}},
		} {
			requestAndAssertCode(http.StatusUnauthorized, t, tc.method, managerToken, tc.body, metaPath...)
			meta := getMeta()
			c.Assert(meta["name"], qt.DeepEquals, original["name"], qt.Commentf(tc.name))
			c.Assert(meta["logo"], qt.DeepEquals, original["logo"], qt.Commentf(tc.name))
			_, hasDescription := meta["description"]
			c.Assert(hasDescription, qt.IsFalse, qt.Commentf(tc.name))
		}
	})

	t.Run("post rejects dotted keys for any role", func(t *testing.T) {
		c := qt.New(t)
		before := getMeta()
		// a planted literal "description.default" would later be picked up by an admin PUT path
		planted := maps.Clone(before)
		planted["description.default"] = "Evil"
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: planted}, metaPath...)
		c.Assert(getMeta(), qt.DeepEquals, before)

		dotted := maps.Clone(before)
		dotted["contact.email"] = "info@example.com"
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
			&apicommon.OrganizationAddMetaRequest{Meta: dotted}, metaPath...)
		c.Assert(getMeta(), qt.DeepEquals, before)
	})

	t.Run("admin can change identity keys", func(t *testing.T) {
		c := qt.New(t)
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, &apicommon.OrganizationAddMetaRequest{
			Meta: map[string]any{"name.default": "Acme Renamed"},
		}, metaPath...)
		name, ok := getMeta()["name"].(map[string]any)
		c.Assert(ok, qt.IsTrue)
		c.Assert(name["default"], qt.Equals, "Acme Renamed")

		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken,
			&apicommon.OrganizationDeleteMetaRequest{Keys: []string{"logo"}}, metaPath...)
		_, ok = getMeta()["logo"]
		c.Assert(ok, qt.IsFalse)
	})
}

// testAddOrgManager makes a new user a manager of orgAddress and returns their token.
func testAddOrgManager(t *testing.T, orgAddress common.Address) string {
	t.Helper()
	managerToken := testCreateUser(t, "managerpassword123")
	managerInfo := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	managerUser, err := testDB.UserByEmail(managerInfo.Email)
	qt.Assert(t, err, qt.IsNil)
	managerUser.Organizations = append(managerUser.Organizations, db.OrganizationUser{
		Address: orgAddress,
		Role:    db.ManagerRole,
	})
	_, err = testDB.SetUser(managerUser)
	qt.Assert(t, err, qt.IsNil)
	return managerToken
}

// TestOrganizationMetaDottedIdentityKey checks that a manager cannot write into an identity key
// through a dotted sub-key, even when a literal field with that name is stored next to it.
func TestOrganizationMetaDottedIdentityKey(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	managerToken := testAddOrgManager(t, orgAddress)
	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}

	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken,
		&apicommon.OrganizationInfo{Name: &apicommon.MultilingualText{"default": "Acme"}},
		"organizations", orgAddress.String())
	// a literal "name.default" field already stored with the value the manager is about to send,
	// which is what made the old literal-key comparison let the dotted write through
	meta := getMeta()
	meta["name.default"] = "Evil"
	c.Assert(testDB.AddOrganizationMeta(orgAddress, meta), qt.IsNil)

	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPut, managerToken,
		&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"name.default": "Evil"}}, metaPath...)
	c.Assert(getMeta()["name"], qt.DeepEquals, map[string]any{"default": "Acme"})
}

// TestOrganizationMetaLegacyIdentityValues checks that a manager can send back an identity value
// in the normalized form GET /organizations returns, while it is stored as a legacy plain string.
func TestOrganizationMetaLegacyIdentityValues(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	managerToken := testAddOrgManager(t, orgAddress)
	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}

	// a legacy plain-string description and a name, stored without the multilingual wrapper
	c.Assert(testDB.AddOrganizationMeta(orgAddress, map[string]any{
		"name":        "Acme",
		"description": "Legacy",
	}), qt.IsNil)

	t.Run("put with normalized value", func(t *testing.T) {
		c := qt.New(t)
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{
				"description": map[string]any{"default": "Legacy"},
				"region":      "Spain",
			}}, metaPath...)
		meta := getMeta()
		c.Assert(meta["description"], qt.Equals, "Legacy")
		c.Assert(meta["region"], qt.Equals, "Spain")
	})

	t.Run("put with only identity keys", func(t *testing.T) {
		c := qt.New(t)
		// every key is stripped before the write, which then sets nothing
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{
				"description": map[string]any{"default": "Legacy"},
			}}, metaPath...)
		c.Assert(getMeta()["description"], qt.Equals, "Legacy")
	})

	t.Run("post with normalized values", func(t *testing.T) {
		c := qt.New(t)
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{
				"name":        map[string]any{"default": "Acme"},
				"description": map[string]any{"default": "Legacy"},
				"region":      "Europe",
			}}, metaPath...)
		meta := getMeta()
		c.Assert(meta["name"], qt.Equals, "Acme")
		c.Assert(meta["description"], qt.Equals, "Legacy")
		c.Assert(meta["region"], qt.Equals, "Europe")
	})

	t.Run("post changing a normalized value is still rejected", func(t *testing.T) {
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{
				"name":        map[string]any{"default": "Evil"},
				"description": map[string]any{"default": "Legacy"},
			}}, metaPath...)
	})
}

// TestOrganizationMetaManagerIdentityDelete checks that a manager may delete identity keys that
// are not stored, which are no-ops, while stored ones stay admin-only.
func TestOrganizationMetaManagerIdentityDelete(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	managerToken := testAddOrgManager(t, orgAddress)

	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken,
		&apicommon.OrganizationInfo{Name: &apicommon.MultilingualText{"default": "Acme"}},
		"organizations", orgAddress.String())

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, managerToken,
		&apicommon.OrganizationDeleteMetaRequest{Keys: []string{"logo", "description"}}, metaPath...)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodDelete, managerToken,
		&apicommon.OrganizationDeleteMetaRequest{Keys: []string{"name.es"}}, metaPath...)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodDelete, managerToken,
		&apicommon.OrganizationDeleteMetaRequest{Keys: []string{"logo", "name"}}, metaPath...)

	meta := requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	c.Assert(meta["name"], qt.DeepEquals, map[string]any{"default": "Acme"})
}

// TestOrganizationMetaRejectsMalformedKeys checks that malformed meta keys and paths are answered
// with 400 on every write path, and leave the stored meta untouched.
func TestOrganizationMetaRejectsMalformedKeys(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken,
		&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"region": "Europe", "tag": "x"}}, metaPath...)
	before := getMeta()

	t.Run("post", func(t *testing.T) {
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{
				"settings": map[string]any{"a.b": 1},
			}}, metaPath...)
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"$set": 1}}, metaPath...)
	})

	t.Run("put", func(t *testing.T) {
		for _, meta := range []map[string]any{
			{"": 1},
			{"a..b": 1},
			{"a.$b": 1},
			{"settings": map[string]any{"a.b": 1}},
			{"tag": 1, "tag.b": 2},
			{"region.es": "Europa"}, // a sub-key of a string value
		} {
			requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, adminToken,
				&apicommon.OrganizationAddMetaRequest{Meta: meta}, metaPath...)
		}
	})

	t.Run("delete", func(t *testing.T) {
		for _, keys := range [][]string{{""}, {"a..b"}, {"tag", "tag.b"}} {
			requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodDelete, adminToken,
				&apicommon.OrganizationDeleteMetaRequest{Keys: keys}, metaPath...)
		}
	})

	t.Run("organization writers", func(t *testing.T) {
		dotted := map[string]any{"a.b": 1}
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, adminToken,
			&apicommon.OrganizationInfo{Meta: dotted}, "organizations", orgAddress.String())
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
			&apicommon.CreateOrganizationRequest{OrganizationInfo: apicommon.OrganizationInfo{
				Type:    string(db.CompanyType),
				Website: "https://dotted-meta.example",
				Meta:    dotted,
			}}, "organizations")
	})

	c.Assert(getMeta(), qt.DeepEquals, before)
}

// TestOrganizationMetaManagedOrgDottedKeys checks that managed-org creation rejects dotted meta
// keys in the request with 400.
func TestOrganizationMetaManagedOrgDottedKeys(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	integratorAddr := testCreateOrganization(t, adminToken)
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
		&apicommon.CreateManagedOrganizationRequest{OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: "https://managed-dotted-meta.example",
			Meta:    map[string]any{"settings": map[string]any{"a.b": 1}},
		}}, "integrator", "organizations")

	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
		&apicommon.CreateManagedOrganizationRequest{OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: "https://managed-dotted-shorthand.example",
			Name:    &apicommon.MultilingualText{"default": "x", "en.US": "y"},
		}}, "integrator", "organizations")
}

// TestMetaPathsConflict covers the overlap check behind the 400 for keys like "a" and "a.b", which
// names the overlapping pair.
func TestMetaPathsConflict(t *testing.T) {
	c := qt.New(t)
	c.Assert(checkMetaPathsConflict([]string{"a", "a.b"}), qt.ErrorMatches, `meta keys "a" and "a.b" conflict with each other`)
	c.Assert(checkMetaPathsConflict([]string{"a.b", "a"}), qt.IsNotNil)
	c.Assert(checkMetaPathsConflict([]string{"a", "a"}), qt.IsNotNil)
	c.Assert(checkMetaPathsConflict([]string{"ab", "a", "a.b.c"}), qt.IsNotNil)
	c.Assert(checkMetaPathsConflict([]string{"ab", "a.c", "b"}), qt.IsNil)
	c.Assert(checkMetaPathsConflict(nil), qt.IsNil)
	// "a-" sorts between "a" and "a.b" in plain string order, but the conflict with "a.b" still counts
	c.Assert(checkMetaPathsConflict([]string{"a", "a-", "a.b"}), qt.ErrorMatches, `meta keys "a" and "a.b" conflict with each other`)
	c.Assert(checkMetaPathsConflict([]string{"a-", "a.b"}), qt.IsNil)
	c.Assert(checkMetaPathsConflict([]string{"a", "a.b.c"}), qt.ErrorMatches, `meta keys "a" and "a.b.c" conflict with each other`)
	// the input order does not change the outcome, and the caller's slice is left as it was
	paths := []string{"a.b.c", "x", "a", "y.z"}
	c.Assert(checkMetaPathsConflict(paths), qt.ErrorMatches, `meta keys "a" and "a.b.c" conflict with each other`)
	c.Assert(paths, qt.DeepEquals, []string{"a.b.c", "x", "a", "y.z"})
}

// TestMetaPathsConflictManyKeys checks that a large set of distinct paths is validated quickly, which
// the pairwise comparison this replaced could not do (tens of seconds for 50k keys).
func TestMetaPathsConflictManyKeys(t *testing.T) {
	c := qt.New(t)
	paths := make([]string, 0, 50000)
	for i := range 50000 {
		paths = append(paths, fmt.Sprintf("group%d.key%d", i%100, i))
	}
	start := time.Now()
	c.Assert(validateMetaPaths(paths), qt.IsNil)
	c.Assert(time.Since(start) < 10*time.Second, qt.IsTrue)
	c.Assert(validateMetaPaths(append(paths, "group7")), qt.IsNotNil)
}

// TestOrganizationMetaDottedKeyNotMatchedToLiteral checks that a PUT key with a dot is never skipped
// as unchanged because a literal top-level field of that name is stored: it is a path, so its
// value is validated and rejected here.
func TestOrganizationMetaDottedKeyNotMatchedToLiteral(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}

	// planted directly in the database, as a legacy literal dotted field
	c.Assert(testDB.AddOrganizationMeta(orgAddress, map[string]any{
		"x.y": map[string]any{"$a": 1},
	}), qt.IsNil)
	before := requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta

	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, adminToken,
		&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"x.y": map[string]any{"$a": 1}}}, metaPath...)
	c.Assert(requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta,
		qt.DeepEquals, before)
}

// TestOrganizationMetaUnchangedNestedValues checks that a value stored with a nested key the request
// validation now refuses is echoed back as is by PUT and POST, while a changed value is still rejected.
func TestOrganizationMetaUnchangedNestedValues(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	managerToken := testAddOrgManager(t, orgAddress)
	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}

	// planted directly in the database, as written before the nested key check existed
	c.Assert(testDB.AddOrganizationMeta(orgAddress, map[string]any{
		"settings": map[string]any{"a.b": 1},
		"price":    map[string]any{"$x": "y"},
		"name":     map[string]any{"default": "Acme", "en.US": "Acme US"},
		"region":   "Europe",
	}), qt.IsNil)
	stored := getMeta()

	for _, tc := range []struct {
		name   string
		method string
		token  string
	}{
		{"manager put", http.MethodPut, managerToken},
		{"manager post", http.MethodPost, managerToken},
		{"admin put", http.MethodPut, adminToken},
		{"admin post", http.MethodPost, adminToken},
	} {
		c.Run(tc.name, func(c *qt.C) {
			requestAndAssertCode(http.StatusOK, t, tc.method, tc.token,
				&apicommon.OrganizationAddMetaRequest{Meta: maps.Clone(stored)}, metaPath...)
			c.Assert(getMeta(), qt.DeepEquals, stored)
		})
	}

	t.Run("changed nested value is still rejected", func(t *testing.T) {
		c := qt.New(t)
		for _, token := range []string{managerToken, adminToken} {
			changed := maps.Clone(stored)
			changed["settings"] = map[string]any{"a.b": 2}
			requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, token,
				&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"settings": map[string]any{"a.b": 2}}}, metaPath...)
			requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, token,
				&apicommon.OrganizationAddMetaRequest{Meta: changed}, metaPath...)
		}
		c.Assert(getMeta(), qt.DeepEquals, stored)
	})

	t.Run("new nested value is still rejected", func(t *testing.T) {
		c := qt.New(t)
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: map[string]any{"tags": map[string]any{"x.y": 1}}}, metaPath...)
		planted := maps.Clone(stored)
		planted["tags"] = map[string]any{"x.y": 1}
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, managerToken,
			&apicommon.OrganizationAddMetaRequest{Meta: planted}, metaPath...)
		c.Assert(getMeta(), qt.DeepEquals, stored)
	})

	t.Run("literal dotted top-level key is still rejected", func(t *testing.T) {
		c := qt.New(t)
		planted := maps.Clone(stored)
		planted["contact.email"] = "info@example.com"
		requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
			&apicommon.OrganizationAddMetaRequest{Meta: planted}, metaPath...)
		c.Assert(getMeta(), qt.DeepEquals, stored)
	})
}

// TestOrganizationIdentityShorthandKeys checks that locale keys of the name, logo and description
// shorthands are validated like the explicit meta keys, on create, update and managed-org create.
func TestOrganizationIdentityShorthandKeys(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	metaPath := []string{"organizations", orgAddress.String(), "meta"}
	getMeta := func() map[string]any {
		return requestAndParse[apicommon.OrganizationMetaResponse](t, http.MethodGet, adminToken, nil, metaPath...).Meta
	}
	before := getMeta()

	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPost, adminToken,
		&apicommon.CreateOrganizationRequest{OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: "https://shorthand-locale.example",
			Name:    &apicommon.MultilingualText{"default": "x", "en.US": "y"},
		}}, "organizations")
	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, adminToken,
		&apicommon.OrganizationInfo{Logo: &apicommon.MultilingualText{"default": "https://example.com/l.png", "a.b": "y"}},
		"organizations", orgAddress.String())
	requestAndAssertError(errors.ErrInvalidUserData, t, http.MethodPut, adminToken,
		&apicommon.OrganizationInfo{Description: &apicommon.MultilingualText{"": "x", "default": "y"}},
		"organizations", orgAddress.String())
	c.Assert(getMeta(), qt.DeepEquals, before)

	// a valid locale map still goes through
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken,
		&apicommon.OrganizationInfo{Name: &apicommon.MultilingualText{"default": "Acme", "es": "Acme ES"}},
		"organizations", orgAddress.String())
	c.Assert(getMeta()["name"], qt.DeepEquals, map[string]any{"default": "Acme", "es": "Acme ES"})
}
