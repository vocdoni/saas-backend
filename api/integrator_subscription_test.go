package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestIntegratorPlanSubscription drives the integrator flow through the proper
// subscription model: the integrator inherits its limits from its subscription
// PLAN (no per-org IntegratorLimits override), and every managed org it creates
// is auto-subscribed to the default plan. It complements TestIntegratorManagedOrgs,
// which enables the integrator via an inline override and never exercises the
// plan-derived limits path (EffectiveIntegratorLimits' plan branch).
func TestIntegratorPlanSubscription(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "integratorplanpass123")
	integratorAddr := testCreateOrganization(t, token)

	// Seed an integrator-enabled plan; its IntegratorLimits are what the integrator
	// should inherit. The ID is the plan's Stripe product ID (SetPlan rejects empty IDs).
	// The plans collection survives DeleteAllDocuments, so delete this plan on cleanup to
	// keep the seeded set at 3.
	const maxManagedOrgs = 2
	integratorPlan := &db.Plan{
		ID:   "prod_test_integrator",
		Name: "Integrator Plan",
		IntegratorLimits: db.IntegratorLimits{
			MaxManagedOrgs:       maxManagedOrgs,
			MaxManagedProcesses:  5,
			MaxManagedCensusSize: 5000,
		},
	}
	c.Assert(testDB.SetPlan(integratorPlan), qt.IsNil)
	integratorPlanID := integratorPlan.ID
	defer func() {
		if err := testDB.DelPlan(&db.Plan{ID: integratorPlanID}); err != nil {
			c.Logf("cleanup plan: %v", err)
		}
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// Subscribe the org to the integrator plan WITHOUT setting an IntegratorLimits
	// override, so both enablement and limits must resolve from the (active) plan.
	setOrganizationSubscription(t, integratorAddr, integratorPlanID)
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.IntegratorLimits, qt.IsNil) // override path must be out of play

	// Integrator info: enabled, and the limits are exactly the plan's.
	info := requestAndParse[apicommon.IntegratorInfoResponse](
		t, http.MethodGet, token, nil, "organizations", integratorAddr.String(), "integrator")
	c.Assert(info.Enabled, qt.IsTrue)
	c.Assert(info.Limits, qt.Not(qt.IsNil))
	c.Assert(info.Limits.MaxManagedOrgs, qt.Equals, maxManagedOrgs)
	c.Assert(info.Usage.ManagedOrgs, qt.Equals, 0)

	// Create managed orgs up to the plan cap.
	managed := make([]common.Address, 0, maxManagedOrgs)
	for i := 0; i < maxManagedOrgs; i++ {
		created := requestAndParse[apicommon.OrganizationInfo](
			t, http.MethodPost, token,
			&apicommon.CreateManagedOrganizationRequest{
				OrganizationInfo: apicommon.OrganizationInfo{
					Type:    string(db.CompanyType),
					Website: fmt.Sprintf("https://managed-plan-%d.example", i),
				},
			},
			"organizations", integratorAddr.String(), "managed")
		c.Assert(created.Address, qt.Not(qt.Equals), common.Address{})
		managed = append(managed, created.Address)
	}

	// One past the plan cap is rejected.
	_, code := testRequest(t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{
				Type:    string(db.CompanyType),
				Website: "https://managed-plan-over.example",
			},
		},
		"organizations", integratorAddr.String(), "managed")
	c.Assert(code, qt.Equals, http.StatusBadRequest) // ErrMaxManagedOrgsReached

	// Usage and the managed list both reflect the created orgs.
	info = requestAndParse[apicommon.IntegratorInfoResponse](
		t, http.MethodGet, token, nil, "organizations", integratorAddr.String(), "integrator")
	c.Assert(info.Usage.ManagedOrgs, qt.Equals, maxManagedOrgs)
	list := requestAndParse[apicommon.ListManagedOrganizations](
		t, http.MethodGet, token, nil, "organizations", integratorAddr.String(), "managed")
	c.Assert(list.Organizations, qt.HasLen, maxManagedOrgs)

	// Each managed org is linked to the integrator and auto-subscribed to the
	// default plan — the subscription model applied to managed orgs.
	defaultPlan, err := testDB.DefaultPlan()
	c.Assert(err, qt.IsNil)
	for _, addr := range managed {
		mo, err := testDB.Organization(addr)
		c.Assert(err, qt.IsNil)
		c.Assert(mo.ManagedBy, qt.Equals, integratorAddr)
		c.Assert(mo.Subscription.PlanID, qt.Equals, defaultPlan.ID)
	}
}
