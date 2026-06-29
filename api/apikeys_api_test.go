package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestAPIKeysAPI exercises the API-key lifecycle and the auth/scope/allowlist enforcement:
// an admin creates a scoped key, the key can call allowlisted endpoints within its scopes,
// is rejected for missing scopes and for non-allowlisted endpoints, and stops working once revoked.
func TestAPIKeysAPI(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "apikeypass123")
	orgAddr := testCreateOrganization(t, token)

	// make the org an integrator (override) so the integrator endpoints are usable
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// create a key scoped to quota:read + managed:read
	createBody := &apicommon.CreateAPIKeyRequest{Label: "ci", Scopes: []string{ScopeQuotaRead, ScopeManagedRead}}
	data, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var created apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &created), qt.IsNil)
	c.Assert(strings.HasPrefix(created.Secret, APIKeyPrefix), qt.IsTrue)
	c.Assert(created.ID, qt.Not(qt.Equals), "")
	apiKey := created.Secret // testRequest puts this in the Bearer header

	// invalid scope is rejected at creation
	_, code = testRequest(t, http.MethodPost, token,
		&apicommon.CreateAPIKeyRequest{Label: "bad", Scopes: []string{"bogus:scope"}},
		"organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// the key works on allowlisted endpoints within its scopes (path-less: the integrator org is
	// resolved from the key itself, no address in the URL)
	_, code = testRequest(t, http.MethodGet, apiKey, nil, "integrator")
	c.Assert(code, qt.Equals, http.StatusOK) // quota:read
	_, code = testRequest(t, http.MethodGet, apiKey, nil, "integrator", "organizations")
	c.Assert(code, qt.Equals, http.StatusOK) // managed:read

	// missing scope → 403 (key lacks managed:write)
	mbody := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType)},
	}
	_, code = testRequest(t, http.MethodPost, apiKey, mbody, "integrator", "organizations")
	c.Assert(code, qt.Equals, http.StatusForbidden)

	// non-allowlisted endpoint → 403 (API keys can't manage API keys)
	_, code = testRequest(t, http.MethodGet, apiKey, nil, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusForbidden)

	// listing with the JWT shows the key (no secret)
	data, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK)
	var list apicommon.ListAPIKeysResponse
	c.Assert(json.Unmarshal(data, &list), qt.IsNil)
	c.Assert(list.APIKeys, qt.HasLen, 1)
	c.Assert(list.APIKeys[0].ID, qt.Equals, created.ID)
	c.Assert(list.APIKeys[0].Prefix, qt.Equals, created.Prefix)

	// revoke with the JWT
	_, code = testRequest(t, http.MethodDelete, token, nil, "organizations", orgAddr.String(), "apikeys", created.ID)
	c.Assert(code, qt.Equals, http.StatusOK)

	// the revoked key no longer authenticates
	_, code = testRequest(t, http.MethodGet, apiKey, nil, "integrator")
	c.Assert(code, qt.Equals, http.StatusUnauthorized)
}

// TestIntegratorAPIKeyPathless verifies the path-less integrator endpoints resolve the integrator
// organization from the API key: a key with the integrator scopes creates and lists managed orgs and
// reads quota with no organization address in the URL, and the managed org is owned by the key's org.
func TestIntegratorAPIKeyPathless(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "pathlesskeypass123")
	orgAddr := testCreateOrganization(t, token)

	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// mint a key with the integrator scopes
	createBody := &apicommon.CreateAPIKeyRequest{
		Label:  "pathless",
		Scopes: []string{ScopeQuotaRead, ScopeManagedRead, ScopeManagedWrite},
	}
	data, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var created apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &created), qt.IsNil)
	apiKey := created.Secret

	// create a managed org with the key — no address in the URL, resolved from the key's own org
	mbody := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://m1.example"},
	}
	createdOrg := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, apiKey, mbody, "integrator", "organizations")
	c.Assert(createdOrg.Address, qt.Not(qt.Equals), common.Address{})

	// the managed org is owned by the key's integrator org
	mo, err := testDB.Organization(createdOrg.Address)
	c.Assert(err, qt.IsNil)
	c.Assert(mo.ManagedBy, qt.Equals, orgAddr)

	// list + quota reflect it, all path-less
	list := requestAndParse[apicommon.ListManagedOrganizations](t, http.MethodGet, apiKey, nil, "integrator", "organizations")
	c.Assert(list.Organizations, qt.HasLen, 1)
	info := requestAndParse[apicommon.IntegratorInfoResponse](t, http.MethodGet, apiKey, nil, "integrator")
	c.Assert(info.Enabled, qt.IsTrue)
	c.Assert(info.Usage.ManagedOrgs, qt.Equals, 1)
}

// TestIntegratorAPIKeyDeletesProcessDraft verifies that a scoped API key (voting:write) can delete
// a draft process of a managed organization, mirroring the create/publish/status write ops which are
// all voting:write. Without DELETE /process/{processId} in the allowlist the call is rejected with 403
// (ErrAPIKeyNotAllowed), which leaves quota-blocked drafts permanently stuck for key-only integrators.
func TestIntegratorAPIKeyDeletesProcessDraft(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "deletedraftpass123")
	orgAddr := testCreateOrganization(t, token)

	// make the org an integrator (override)
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// mint a key with the scopes needed to create a managed org and act on its processes
	createBody := &apicommon.CreateAPIKeyRequest{
		Label:  "voting",
		Scopes: []string{ScopeManagedWrite, ScopeVotingWrite},
	}
	data, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var created apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &created), qt.IsNil)
	apiKey := created.Secret

	// create a managed org with the key; the key owner becomes its admin (org.Creator)
	mbody := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://md.example"},
	}
	managed := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, apiKey, mbody, "integrator", "organizations")
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})

	// seed a draft under the managed org directly (draft creation has its own quota that is
	// orthogonal to delete authorization, which is what this test exercises). The key owner is
	// admin of the managed org via org.Creator, exactly as for the publish/status handlers.
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: managed.Address,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Draft to delete"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 10,
			Questions: []db.Question{{
				Title: db.MultiLangString{"default": "Approve?"},
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "No"}, Value: 0},
					{Title: db.MultiLangString{"default": "Yes"}, Value: 1},
				},
			}},
			VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
			ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(draftID.IsZero(), qt.IsFalse)

	// the key (voting:write) can delete the managed org's draft
	_, code = testRequest(t, http.MethodDelete, apiKey, nil, "process", draftID.Hex())
	c.Assert(code, qt.Equals, http.StatusOK)

	// the draft is gone
	_, err = testDB.Process(draftID)
	c.Assert(err, qt.Equals, db.ErrNotFound)

	// a key without voting:write cannot delete (insufficient scope → 403)
	noScopeBody := &apicommon.CreateAPIKeyRequest{Label: "noscope", Scopes: []string{ScopeManagedRead}}
	data, code = testRequest(t, http.MethodPost, token, noScopeBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var noScope apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &noScope), qt.IsNil)
	_, code = testRequest(t, http.MethodDelete, noScope.Secret, nil, "process", primitive.NewObjectID().Hex())
	c.Assert(code, qt.Equals, http.StatusForbidden)
}

// TestAPIKeysRequireIntegrator verifies that API keys can only be created by integrator
// organizations: a plain (non-integrator) org admin is rejected with 403, while the same
// org once enabled as an integrator (override) is allowed.
func TestAPIKeysRequireIntegrator(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "apikeyintegratorpass123")
	orgAddr := testCreateOrganization(t, token) // plain org, not an integrator

	// a plain org admin cannot mint keys (integrator-only)
	createBody := &apicommon.CreateAPIKeyRequest{Label: "ci", Scopes: []string{ScopeQuotaRead}}
	_, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusForbidden) // ErrNotAnIntegrator

	// a malformed {address} path param is rejected with 400, not silently treated as the zero address
	_, code = testRequest(t, http.MethodPost, token, createBody, "organizations", "not-an-address", "apikeys")
	c.Assert(code, qt.Equals, http.StatusBadRequest)

	// enable the org as an integrator (override) and creation now succeeds
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	data, code := testRequest(t, http.MethodPost, token, createBody, "organizations", orgAddr.String(), "apikeys")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
	var created apicommon.CreateAPIKeyResponse
	c.Assert(json.Unmarshal(data, &created), qt.IsNil)
	c.Assert(strings.HasPrefix(created.Secret, APIKeyPrefix), qt.IsTrue)
}
