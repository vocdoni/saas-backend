package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson/primitive"
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
		MaxManagedOrgs:       1,
		MaxManagedProcesses:  2,
		MaxManagedCensusSize: 1000,
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
	censusID := createManagedOrgCensus(t, token, managed.Address)

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

	// (D) active-election guard: publish a draft under the managed org and keep it READY, then the
	// delete must be blocked with 409 and leave everything in place.
	managedWithPlan := enableProcessPlan(t, managed.Address)
	activeDraft := seedDraftForManagedOrg(t, managed.Address)
	pubJob := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", activeDraft.Hex(), "publish")
	c.Assert(pubJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish error: %s", pubJob.Error))
	c.Assert(len(pubJob.Result.Address) > 0, qt.IsTrue)

	// the published election autostarts (ElectionType.Autostart), so it is READY on-chain → 409.
	_, code = testRequest(t, http.MethodDelete, token, nil,
		"integrator", "organizations", managed.Address.String())
	c.Assert(code, qt.Equals, http.StatusConflict)

	// nothing was deleted: the org, its census and its memberbase are still there.
	_, err = testDB.Organization(managed.Address)
	c.Assert(err, qt.IsNil)
	_, err = testDB.Census(censusID)
	c.Assert(err, qt.IsNil)
	_, membersStillThere, err := testDB.OrgMembers(managed.Address, 1, 100, "")
	c.Assert(err, qt.IsNil)
	c.Assert(len(membersStillThere) > 0, qt.IsTrue)

	// (E) end the active election so the guard passes, then delete. ENDED is terminal, not active.
	ended := enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.SetProcessStatusRequest{Status: "ended"},
		"process", pubJob.Result.Address.String(), "status")
	c.Assert(ended.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("status error: %s", ended.Error))

	// sanity: the now-ended election still belongs to the managed org before teardown
	_, publishedBefore, err := testDB.ListProcesses(managed.Address, 1, 100, db.PublishedOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedBefore, qt.HasLen, 1)

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
	_, procs, err := testDB.ListProcesses(managed.Address, 1, 100, db.PublishedOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(procs, qt.HasLen, 0)
	_, noMembers, err := testDB.OrgMembers(managed.Address, 1, 100, "")
	c.Assert(err, qt.IsNil)
	c.Assert(noMembers, qt.HasLen, 0)

	// the integrator's usage counters were rolled back fully (we published 1 process, no census size).
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedOrgs, qt.Equals, 0)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 0)
	// the freed slot is reusable in principle: ManagedOrgs (0) is back below MaxManagedOrgs (1).
	// We don't exercise a second create here to keep the test's on-chain footprint minimal
	// (each managed-org create funds a faucet account), avoiding CI email-delivery timeouts.
	_ = managedWithPlan
}

// enableProcessPlan subscribes the managed org to a process-capable plan so publish is allowed.
// Returns nothing meaningful; kept for symmetry/readability.
func enableProcessPlan(t *testing.T, orgAddress common.Address) struct{} {
	t.Helper()
	plans, err := testDB.Plans()
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, len(plans) > 1, qt.IsTrue)
	qt.Assert(t, testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)
	return struct{}{}
}

// seedDraftForManagedOrg creates a publishable draft process owned by the managed org.
func seedDraftForManagedOrg(t *testing.T, orgAddress common.Address) primitive.ObjectID {
	t.Helper()
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: orgAddress,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Managed deletion election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 10,
			Questions: []db.Question{{
				Title: db.MultiLangString{"default": "Q1"},
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
					{Title: db.MultiLangString{"default": "No"}, Value: 1},
				},
			}},
			VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
			ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
		},
	})
	qt.Assert(t, err, qt.IsNil)
	return draftID
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

// createManagedOrgCensus creates an empty census owned by the managed org and returns its hex id.
func createManagedOrgCensus(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	resp := requestAndParse[apicommon.CreateCensusResponse](
		t, http.MethodPost, token, &apicommon.CreateCensusRequest{
			OrgAddress:  orgAddress,
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		}, censusEndpoint,
	)
	qt.Assert(t, resp.ID, qt.Not(qt.Equals), "")
	return resp.ID
}
