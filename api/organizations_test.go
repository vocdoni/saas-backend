package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

func TestCreateOrganizationHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and get token
	token := testCreateUser(t, testPass)

	// Test creating an organization with valid data
	orgInfo := &apicommon.OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: "https://example.com",
	}
	resp, code := testRequest(t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var createdOrg apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &createdOrg), qt.IsNil)
	c.Assert(createdOrg.Address, qt.Not(qt.Equals), common.Address{})
	c.Assert(createdOrg.Type, qt.Equals, string(db.CompanyType))
	c.Assert(createdOrg.Website, qt.Equals, "https://example.com")

	// Test creating organization without authentication
	resp, code = testRequest(t, http.MethodPost, "", orgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
	c.Assert(string(resp), qt.Contains, "40001")

	// Test creating organization with invalid body
	resp, code = testRequest(t, http.MethodPost, token, "invalid body", organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "40004")

	// Test creating organization with invalid type
	invalidOrgInfo := &apicommon.OrganizationInfo{
		Type:    "invalid_type",
		Website: "https://example.com",
	}
	resp, code = testRequest(t, http.MethodPost, token, invalidOrgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "invalid organization type")
}

func TestCreateSubOrganizationHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and get token
	token := testCreateUser(t, testPass)

	// Create a parent organization
	parentOrgAddress := testCreateOrganization(t, token)

	// Test creating a suborganization - should fail because free plan doesn't allow suborganizations (SubOrgs: 0)
	subOrgInfo := &apicommon.OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: "https://suborganization.com",
		Parent: &apicommon.OrganizationInfo{
			Address: parentOrgAddress,
		},
	}
	_, code := testRequest(t, http.MethodPost, token, subOrgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test creating suborganization with non-existent parent
	nonExistentParent := common.HexToAddress("0x0000000000000000000000000000000000000001")
	invalidSubOrgInfo := &apicommon.OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: "https://invalid.com",
		Parent: &apicommon.OrganizationInfo{
			Address: nonExistentParent,
		},
	}
	_, code = testRequest(t, http.MethodPost, token, invalidSubOrgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test creating suborganization when user is not admin of parent
	// (same error as above - permission check happens first)
	anotherUserToken := testCreateUser(t, "anotherpassword")
	anotherUserOrgAddress := testCreateOrganization(t, anotherUserToken)
	subOrgByNonAdmin := &apicommon.OrganizationInfo{
		Type:    string(db.CompanyType),
		Website: "https://unauthorized.com",
		Parent: &apicommon.OrganizationInfo{
			Address: anotherUserOrgAddress,
		},
	}
	_, code = testRequest(t, http.MethodPost, anotherUserToken, subOrgByNonAdmin, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestOrganizationInfoHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Test getting info for non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code := testRequest(t, http.MethodGet, token, nil, "organizations", nonExistentAddr.String())
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test without authentication (should still work as this is a public endpoint)
	resp, code := testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
}

func TestUpdateOrganizationHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Test updating organization with valid data
	updateInfo := &apicommon.OrganizationInfo{
		Website:   "https://updated.com",
		Subdomain: "updated-subdomain",
		Color:     "#FF5733",
		Size:      "medium",
		Country:   "US",
		Timezone:  "America/New_York",
	}
	resp, code := testRequest(t, http.MethodPut, token, updateInfo, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify the update
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK)
	var updatedOrg apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &updatedOrg), qt.IsNil)
	c.Assert(updatedOrg.Website, qt.Equals, "https://updated.com")
	c.Assert(updatedOrg.Subdomain, qt.Equals, "updated-subdomain")
	c.Assert(updatedOrg.Color, qt.Equals, "#FF5733")
	c.Assert(updatedOrg.Size, qt.Equals, "medium")

	// Test updating without authentication
	_, code = testRequest(t, http.MethodPut, "", updateInfo, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test updating with invalid body
	_, code = testRequest(t, http.MethodPut, token, "invalid body", "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test updating organization when user is not admin
	anotherUserToken := testCreateUser(t, "anotherpassword")
	_, code = testRequest(t, http.MethodPut, anotherUserToken, updateInfo, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test updating non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code = testRequest(t, http.MethodPut, token, updateInfo, "organizations", nonExistentAddr.String())
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test updating active status
	activeUpdateInfo := &apicommon.OrganizationInfo{
		Active: false,
	}
	resp, code = testRequest(t, http.MethodPut, token, activeUpdateInfo, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify active status update
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK)
	var orgWithUpdatedStatus apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &orgWithUpdatedStatus), qt.IsNil)
	c.Assert(orgWithUpdatedStatus.Active, qt.IsFalse)
}

func TestOrganizationsTypesHandler(t *testing.T) {
	c := qt.New(t)

	// Test getting organization types (no authentication required)
	resp, code := testRequest(t, http.MethodGet, "", nil, organizationTypesEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var typesList apicommon.OrganizationTypeList
	c.Assert(json.Unmarshal(resp, &typesList), qt.IsNil)
	c.Assert(typesList.Types, qt.Not(qt.HasLen), 0)

	// Verify some expected types exist
	typeMap := make(map[string]string)
	for _, orgType := range typesList.Types {
		typeMap[orgType.Type] = orgType.Name
	}
	c.Assert(typeMap[string(db.CompanyType)], qt.Not(qt.Equals), "")
	c.Assert(typeMap[string(db.CooperativeType)], qt.Not(qt.Equals), "")
	c.Assert(typeMap[string(db.AssociationType)], qt.Not(qt.Equals), "")
}

func TestOrganizationSubscriptionHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	org.Counters.Processes = 5
	org.Counters.SentEmails = 3
	org.Counters.SentSMS = 2
	org.Subscription.BillingPeriod = db.BillingPeriodAnnual
	org.Subscription.StartDate = time.Now().UTC().Add(-time.Hour)
	org.Subscription.RenewalDate = time.Now().UTC().Add(time.Hour)
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	c.Assert(testDB.UpsertUsageSnapshot(&db.UsageSnapshot{
		OrgAddress:    orgAddress,
		PeriodStart:   org.Subscription.StartDate,
		PeriodEnd:     org.Subscription.RenewalDate,
		BillingPeriod: org.Subscription.BillingPeriod,
		Baseline: db.UsageSnapshotBaseline{
			Processes:  4,
			SentEmails: 2,
			SentSMS:    1,
		},
	}), qt.IsNil)

	// Test getting subscription info
	resp, code := testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "subscription")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var subInfo apicommon.OrganizationSubscriptionInfo
	c.Assert(json.Unmarshal(resp, &subInfo), qt.IsNil)
	c.Assert(subInfo.Plan, qt.Not(qt.IsNil))
	c.Assert(subInfo.SubscriptionDetails, qt.Not(qt.IsNil))
	c.Assert(subInfo.Usage, qt.Not(qt.IsNil))
	c.Assert(subInfo.PeriodUsage, qt.Not(qt.IsNil))

	// Test without authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "subscription")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test when user is not admin
	anotherUserToken := testCreateUser(t, "anotherpassword")
	_, code = testRequest(t, http.MethodGet, anotherUserToken, nil, "organizations", orgAddress.String(), "subscription")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code = testRequest(t, http.MethodGet, token, nil, "organizations", nonExistentAddr.String(), "subscription")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestOrganizationCensusesHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Test getting censuses (should return empty list initially)
	resp, code := testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "censuses")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var censuses apicommon.OrganizationCensuses
	c.Assert(json.Unmarshal(resp, &censuses), qt.IsNil)
	c.Assert(censuses.Censuses, qt.HasLen, 0)

	// Test without authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "censuses")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test when user is not admin
	anotherUserToken := testCreateUser(t, "anotherpassword")
	_, code = testRequest(t, http.MethodGet, anotherUserToken, nil, "organizations", orgAddress.String(), "censuses")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code = testRequest(t, http.MethodGet, token, nil, "organizations", nonExistentAddr.String(), "censuses")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestOrganizationCreateTicketHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Get user info
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var userInfo apicommon.UserInfo
	c.Assert(json.Unmarshal(resp, &userInfo), qt.IsNil)
	t.Logf("User ID: %d\n", userInfo.ID)

	// Test creating a valid ticket
	ticketReq := &apicommon.CreateOrganizationTicketRequest{
		TicketType:  "technical",
		Title:       "Test ticket",
		Description: "This is a test ticket description",
	}
	resp, code = testRequest(t, http.MethodPost, token, ticketReq, "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Check that cc'ed email arrives correctly to the mailbox
	mailBody := waitForEmail(t, apicommon.SupportEmail)
	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`(?i)\s(You have a new support request)\s`))

	// Test without authentication
	_, code = testRequest(t, http.MethodPost, "", ticketReq, "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with invalid body
	_, code = testRequest(t, http.MethodPost, token, "invalid body", "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test with empty title
	invalidTicket := &apicommon.CreateOrganizationTicketRequest{
		TicketType:  "technical",
		Title:       "",
		Description: "Description only",
	}
	resp, code = testRequest(t, http.MethodPost, token, invalidTicket, "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "title and description are required")

	// Test with empty description
	invalidTicket = &apicommon.CreateOrganizationTicketRequest{
		TicketType:  "technical",
		Title:       "Title only",
		Description: "",
	}
	resp, code = testRequest(t, http.MethodPost, token, invalidTicket, "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "title and description are required")

	// Test when user has no role in organization
	anotherUserToken := testCreateUser(t, "anotherpassword")
	_, code = testRequest(t, http.MethodPost, anotherUserToken, ticketReq, "organizations", orgAddress.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code = testRequest(t, http.MethodPost, token, ticketReq, "organizations", nonExistentAddr.String(), "ticket")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestOrganizationJobsHandler(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Test getting jobs (should return empty list initially)
	resp, code := testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "jobs")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var jobsResp apicommon.JobsResponse
	c.Assert(json.Unmarshal(resp, &jobsResp), qt.IsNil)
	c.Assert(jobsResp.Jobs, qt.HasLen, 0)
	c.Assert(jobsResp.Pagination, qt.Not(qt.IsNil))

	// Test with pagination parameters
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "jobs?page=1&limit=5")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test with job type filter
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(),
		fmt.Sprintf("jobs?type=%s", string(db.JobTypeOrgMembers)))
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(),
		fmt.Sprintf("jobs?type=%s", string(db.JobTypeCensusParticipants)))
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Test with invalid job type filter
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "jobs?type=invalid_type")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, "invalid job type")

	// Test without authentication
	_, code = testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String(), "jobs")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test when user is not admin or manager
	anotherUserToken := testCreateUser(t, "anotherpassword")
	_, code = testRequest(t, http.MethodGet, anotherUserToken, nil, "organizations", orgAddress.String(), "jobs")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)

	// Test with non-existent organization
	nonExistentAddr := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, code = testRequest(t, http.MethodGet, token, nil, "organizations", nonExistentAddr.String(), "jobs")
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// Test with invalid pagination parameters
	_, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "jobs?page=invalid")
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	_, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String(), "jobs?limit=invalid")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
}

func TestOrganizationWithOptionalFields(t *testing.T) {
	c := qt.New(t)

	// Create a user and get token
	token := testCreateUser(t, testPass)

	// Test creating organization with all optional fields
	orgInfo := &apicommon.OrganizationInfo{
		Type:           string(db.AssociationType),
		Website:        "https://fullexample.com",
		Size:           "large",
		Color:          "#00FF00",
		Country:        "ES",
		Subdomain:      "fullexample",
		Timezone:       "Europe/Madrid",
		Active:         true,
		Communications: true,
	}
	resp, code := testRequest(t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var createdOrg apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &createdOrg), qt.IsNil)
	c.Assert(createdOrg.Address, qt.Not(qt.Equals), common.Address{})
	c.Assert(createdOrg.Type, qt.Equals, string(db.AssociationType))
	c.Assert(createdOrg.Website, qt.Equals, "https://fullexample.com")
	c.Assert(createdOrg.Size, qt.Equals, "large")
	c.Assert(createdOrg.Color, qt.Equals, "#00FF00")
	c.Assert(createdOrg.Country, qt.Equals, "ES")
	c.Assert(createdOrg.Subdomain, qt.Equals, "fullexample")
	c.Assert(createdOrg.Timezone, qt.Equals, "Europe/Madrid")
	c.Assert(createdOrg.Active, qt.IsTrue)
	c.Assert(createdOrg.Communications, qt.IsTrue)
}

func TestOrganizationPartialUpdate(t *testing.T) {
	c := qt.New(t)

	// Create a user and organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Get initial organization state
	resp, code := testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK)
	var initialOrg apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &initialOrg), qt.IsNil)

	// Update only one field
	partialUpdate := &apicommon.OrganizationInfo{
		Website: "https://partialupdate.com",
	}
	resp, code = testRequest(t, http.MethodPut, token, partialUpdate, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Verify only the website was updated
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK)
	var updatedOrg apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(resp, &updatedOrg), qt.IsNil)
	c.Assert(updatedOrg.Website, qt.Equals, "https://partialupdate.com")
	c.Assert(updatedOrg.Type, qt.Equals, initialOrg.Type)
	c.Assert(updatedOrg.Address, qt.Equals, initialOrg.Address)
}

func TestCreateOrganizationWithDifferentTypes(t *testing.T) {
	c := qt.New(t)

	// Create a user
	token := testCreateUser(t, testPass)

	// Test creating organizations with different valid types
	types := []db.OrganizationType{
		db.CompanyType,
		db.CooperativeType,
		db.AssociationType,
		db.GovernmentType,
		db.OthersType,
	}

	for _, orgType := range types {
		orgInfo := &apicommon.OrganizationInfo{
			Type:    string(orgType),
			Website: fmt.Sprintf("https://%s.com", orgType),
		}
		resp, code := testRequest(t, http.MethodPost, token, orgInfo, organizationsEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("failed for type %s, response: %s", orgType, resp))

		var createdOrg apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &createdOrg), qt.IsNil)
		c.Assert(createdOrg.Type, qt.Equals, string(orgType))
	}
}
