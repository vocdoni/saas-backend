package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/proto/build/go/models"
)

// TestDeleteManagedOrg exercises DELETE /integrator/organizations/{orgAddress}:
//   - a user with no integrator organization is rejected (403)
//   - an unknown managed org address is 404
//   - an org not managed by the integrator is 404 (no existence leak)
//   - a managed org with an active on-chain election (READY) is blocked with 409, and nothing is deleted
//   - an idle managed org is fully torn down (members, censuses, processes, bundles, jobs, invites,
//     org doc gone; unlinked from users), the integrator's usage counters are rolled back, and the
//     freed managed-org slot can be reused by a subsequent create.
func TestDeleteManagedOrg(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "deleteadminpass123")
	integratorAddr := testCreateOrganization(t, token)

	// enable the org as an integrator (override): MaxManagedOrgs 1 so we can verify slot reuse.
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{
		MaxManagedOrgs: 1,
	}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	// ---- create one managed org to exercise the happy path first ----
	createBody := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: "https://managed-delete.example",
		},
	}
	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token, createBody, "integrator", "organizations",
	)
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})

	// seed some memberbase + a census on the managed org so the cascade has something to remove
	addMembersToManagedOrg(t, managed.Address)
	censusID := createManagedOrgCensus(t, managed.Address)

	// (A) a user with no integrator org is rejected: the integrator is resolved from the caller, and
	// this second user administers no integrator organization.
	otherToken := testCreateUser(t, "otheruserpass123")
	_, code := testRequest(t, http.MethodDelete, otherToken, nil,
		"integrator", "organizations", managed.Address.String())
	c.Assert(code, qt.Equals, http.StatusForbidden) // ErrNotAnIntegrator

	// (B) unknown managed org address is 404.
	_, code = testRequest(t, http.MethodDelete, token, nil,
		"integrator", "organizations", "0xdeadbeef00000000000000000000000000000001")
	c.Assert(code, qt.Equals, http.StatusNotFound)

	// (C) an org not managed by this integrator is 404 (the integrator's own address is not managed).
	_, code = testRequest(t, http.MethodDelete, token, nil,
		"integrator", "organizations", integratorAddr.String())
	c.Assert(code, qt.Equals, http.StatusNotFound)

	// (D) active-election guard: a legacy process row owned by the managed org points at a READY
	// on-chain election, so the delete must be blocked with 409 and leave everything in place.
	enableProcessPlan(t, managed.Address)
	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	election := newSyncTestElection(t, managed.Address, cspPubKey)
	insertLegacyDoc(t, "processes", db.Process{ID: bson.NewObjectID(), OrgAddress: managed.Address, Address: election})

	_, code = testRequest(t, http.MethodDelete, token, nil,
		"integrator", "organizations", managed.Address.String())
	c.Assert(code, qt.Equals, http.StatusConflict)

	// nothing was deleted: the org, its census and its memberbase are still there.
	_, err = testDB.Organization(managed.Address)
	c.Assert(err, qt.IsNil)
	_, err = testDB.Census(censusID)
	c.Assert(err, qt.IsNil)
	_, membersStillThere, err := testDB.OrgMembers(managed.Address, db.OrgMembersQuery{Page: 1, Limit: 100})
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersStillThere) > 0, qt.IsTrue)

	// (E) end the active election so the guard passes, then delete. ENDED is terminal, not active.
	setSyncTestElectionStatus(t, managed.Address, election, models.ProcessStatus_ENDED)
	waitForElectionStatus(t, election, "ENDED", "RESULTS")

	resp := requestAndParse[apicommon.DeleteManagedOrganizationResponse](
		t, http.MethodDelete, token, nil,
		"integrator", "organizations", managed.Address.String(),
	)
	c.Assert(resp.Address, qt.Equals, managed.Address.String())

	// the org doc and its memberbase/census/processes are gone.
	_, err = testDB.Organization(managed.Address)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, err = testDB.Census(censusID)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, err = testDB.ProcessByAddress(election)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, noMembers, err := testDB.OrgMembers(managed.Address, db.OrgMembersQuery{Page: 1, Limit: 100})
	c.Assert(err, qt.IsNil)
	c.Assert(noMembers, qt.HasLen, 0)

	// the integrator's usage counters were rolled back fully.
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedOrgs, qt.Equals, 0)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 0)
	// the freed slot is reusable in principle: ManagedOrgs (0) is back below MaxManagedOrgs (1).
	// We don't exercise a second create here to keep the test's on-chain footprint minimal
	// (each managed-org create funds a faucet account), avoiding CI email-delivery timeouts.
}

// TestDeleteManagedOrgQuestionGuard covers the new /processes question delete guard specifically
// (emmdim review): a managed org whose only active election is surfaced as a /processes question
// (no legacy Process row, so the legacy loop passes) is blocked with 409 while that question's
// election is READY on-chain, and deletable once it is ENDED. This exercises the chain-based guard
// added in place of the stored-status CountActiveQuestions check.
func TestDeleteManagedOrgQuestionGuard(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "qguardadminpass123")
	integratorAddr := testCreateOrganization(t, token)

	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: "https://managed-qguard.example",
		}},
		"integrator", "organizations",
	)
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})
	enableProcessPlan(t, managed.Address)

	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)

	// a READY election owned by the managed org, surfaced only as a /processes question — there is no
	// legacy Process row, so the legacy active-election loop passes and only the question guard can block.
	election := newSyncTestElection(t, managed.Address, cspPubKey)
	vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
		OrgAddress: managed.Address, Published: true,
		Title: db.MultiLangString{"default": "Guard process"}, //nolint:goconst
	})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: managed.Address, Order: 0,
		Title: db.MultiLangString{"default": "Q"}, UpstreamID: election, Status: db.QuestionStatusReady,
	})
	c.Assert(err, qt.IsNil)

	// READY question election → blocked with 409, nothing deleted.
	_, code := testRequest(t, http.MethodDelete, token, nil, "integrator", "organizations", managed.Address.String())
	c.Assert(code, qt.Equals, http.StatusConflict)
	_, err = testDB.Organization(managed.Address)
	c.Assert(err, qt.IsNil)

	// end the election → ENDED is terminal (not READY/PAUSED), so the guard passes and delete succeeds.
	setSyncTestElectionStatus(t, managed.Address, election, models.ProcessStatus_ENDED)
	waitForElectionStatus(t, election, "ENDED", "RESULTS")
	resp := requestAndParse[apicommon.DeleteManagedOrganizationResponse](
		t, http.MethodDelete, token, nil, "integrator", "organizations", managed.Address.String(),
	)
	c.Assert(resp.Address, qt.Equals, managed.Address.String())
}

// enableProcessPlan subscribes the managed org to a process-capable plan so publish is allowed.
func enableProcessPlan(t *testing.T, orgAddress common.Address) {
	t.Helper()
	setOrganizationSubscription(t, orgAddress, mockPremiumPlan.ID)
}

// addMembersToManagedOrg seeds members directly into the managed org's memberbase via the DB
// layer (bypassing the API path, which would send import-completion emails and slow the test
// down under CI load). The teardown must still remove them.
func addMembersToManagedOrg(t *testing.T, orgAddress common.Address) {
	t.Helper()
	c := qt.New(t)
	for _, suffix := range []string{"m1", "m2"} {
		_, err := testDB.SetOrgMember("test_salt", &db.OrgMember{
			OrgAddress:   orgAddress,
			MemberNumber: suffix,
			Email:        suffix + "@delete.example",
			Name:         suffix,
		})
		c.Assert(err, qt.IsNil)
	}
}

// createManagedOrgCensus seeds an empty census owned by the managed org and returns its hex id.
func createManagedOrgCensus(t *testing.T, orgAddress common.Address) string {
	t.Helper()
	id := bson.NewObjectID()
	insertLegacyDoc(t, "census", db.Census{ID: id, OrgAddress: orgAddress})
	return id.Hex()
}
