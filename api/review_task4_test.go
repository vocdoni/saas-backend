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

// TestPublicOrganizationInfoStripsSensitiveFields verifies that the public,
// unauthenticated GET /organizations/{address} endpoint does not leak the Stripe
// billing email (carried in the Subscription block), which is only exposed to org
// admins through the dedicated subscription endpoint (H8).
func TestPublicOrganizationInfoStripsSensitiveFields(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)

	// hit the public endpoint with no auth token
	data, code := testRequest(t, http.MethodGet, "", nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", data))

	var orgInfo apicommon.OrganizationInfo
	c.Assert(json.Unmarshal(data, &orgInfo), qt.IsNil)

	// the org must still be identifiable, but the Subscription block (which carries
	// the Stripe billing email and payment details) must be stripped
	c.Assert(orgInfo.Address, qt.Equals, orgAddress)
	c.Assert(orgInfo.Subscription, qt.IsNil,
		qt.Commentf("public endpoint must not expose the Stripe billing email"))
}

// TestPublishCensusUsesCSPKey verifies that publishing a census stores the CSP
// signer public key as the census root (the key that actually authorizes voter
// ballots), not the blockchain account key (H4).
func TestPublishCensusUsesCSPKey(t *testing.T) {
	c := qt.New(t)

	authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName}

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	censusID := postCensus(t, adminToken, orgAddress, authFields, twoFaEmail)

	published := requestAndParse[apicommon.PublishedCensusResponse](t, http.MethodPost, adminToken, nil,
		censusEndpoint, censusID, "publish")
	c.Assert(published.Root, qt.Not(qt.Equals), internal.HexBytes{})

	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	c.Assert(published.Root.String(), qt.Equals, cspPubKey.String(),
		qt.Commentf("census root must be the CSP signer key, not the blockchain account key"))
}

// TestCreateProcessCensusOnly verifies that a draft process can be created by
// supplying only a censusId (no explicit orgAddress), which the documented API
// supports by resolving the organization from the census (M20).
func TestCreateProcessCensusOnly(t *testing.T) {
	c := qt.New(t)

	authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName}

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	censusID := postCensus(t, adminToken, orgAddress, authFields, twoFaEmail)
	// the census must be published before a process can reference it
	requestAndParse[apicommon.PublishedCensusResponse](t, http.MethodPost, adminToken, nil,
		censusEndpoint, censusID, "publish")

	// subscribe the org to a plan that allows drafts (the free plan allows 0)
	err := testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          mockEssentialPlan.ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(time.Hour * 24),
		LastPaymentDate: time.Now(),
		Active:          true,
	})
	c.Assert(err, qt.IsNil)

	var censusIDBytes internal.HexBytes
	c.Assert(censusIDBytes.ParseString(censusID), qt.IsNil)

	// census-only request: no orgAddress, no address
	data, code := testRequest(t, http.MethodPost, adminToken,
		&apicommon.CreateProcessRequest{CensusID: censusIDBytes}, processCreateEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", data))

	var processID string
	c.Assert(json.Unmarshal(data, &processID), qt.IsNil)
	c.Assert(processID, qt.Not(qt.Equals), "")
}

// TestOrgMembersSearchEscapesRegex verifies that regex metacharacters in the
// member-search query are treated as literal text rather than being compiled as
// a regular expression, so malformed patterns cannot 500 the endpoint or cause
// ReDoS (M22).
func TestOrgMembersSearchEscapesRegex(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	postOrgMembers(t, adminToken, orgAddress, newOrgMembers(3)...)

	// an unbalanced '(' is an invalid regular expression; before escaping it made
	// MongoDB reject the query and the handler return 500. It matches no member
	// literally, so the escaped query must return 200 with zero results.
	data, code := testRequest(t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members?search=(")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", data))

	var resp apicommon.OrganizationMembersResponse
	c.Assert(json.Unmarshal(data, &resp), qt.IsNil)
	c.Assert(resp.Members, qt.HasLen, 0,
		qt.Commentf("regex metacharacter must be matched literally, not compiled"))
}
