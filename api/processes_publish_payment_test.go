package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// newIntegratorWithManagedOrg provisions an integrator organization on a plan that allows
// managed organizations, and returns its address together with one managed organization
// created under it. planID must be unique per test; the plan is removed on cleanup.
func newIntegratorWithManagedOrg(
	t *testing.T, token, planID string,
) (integrator, managedOrg common.Address) {
	t.Helper()
	c := qt.New(t)
	integratorAddr := testCreateOrganization(t, token)

	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.Integrator = true
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)
	integratorPlan := &db.Plan{
		ID:           planID,
		Name:         "Wallet Integrator",
		Organization: db.PlanLimits{MaxProcesses: 5, MaxCensus: 10_000, MaxDuration: 30, MaxDrafts: 5},
		VotingTypes:  db.VotingTypes{Single: true, Multiple: true},
		Features:     db.Features{TwoFaEmail: 10_000, TwoFaSms: 10_000},
	}
	c.Assert(testDB.SetPlan(integratorPlan), qt.IsNil)
	t.Cleanup(func() { _ = testDB.DelPlan(&db.Plan{ID: integratorPlan.ID}) })
	c.Assert(testDB.SetOrganizationSubscription(integratorAddr, &db.OrganizationSubscription{
		PlanID: integratorPlan.ID, StartDate: time.Now(), Active: true,
	}), qt.IsNil)

	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token, &apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{
				Type: string(db.CompanyType), Website: fmt.Sprintf("https://wallet-%d.example", time.Now().UnixNano()),
			},
		}, "integrator", "organizations")
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})
	return integratorAddr, managed.Address
}
