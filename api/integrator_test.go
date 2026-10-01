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
	"github.com/vocdoni/saas-backend/errors"
)

// TestIntegratorManagedOrgs exercises the integrator layer: a non-integrator org is
// rejected, an integrator can create managed orgs up to its quota, the integrator info
// and managed-org list reflect usage, and publishing under a managed org enforces the
// integrator's aggregate process quota.
func TestIntegratorManagedOrgs(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "integratorpass123")
	integratorAddr := testCreateOrganization(t, token)

	// rejection: a user with no integrator org cannot create managed orgs. The integrator org is
	// resolved from the session (path-less), and integratorAddr is not an integrator yet here.
	body := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://managed.example"},
	}
	_, code := testRequest(t, http.MethodPost, token, body, "integrator", "organizations")
	c.Assert(code, qt.Equals, http.StatusForbidden) // ErrNotAnIntegrator

	// enable integrator with an override (MaxManagedOrgs); the aggregate process/census
	// caps come from the integrator's subscription plan top-level limits below.
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	// subscribe the integrator to a plan whose top-level limits bound managed publishing:
	// MaxProcesses 1, MaxCensus 1000. Managed orgs also draw their draft cap from it.
	integratorPlan := &db.Plan{
		ID:           "prod_test_integrator_caps",
		Name:         "Integrator Caps",
		Organization: db.PlanLimits{MaxDrafts: 10, MaxProcesses: 1, MaxCensus: 1000, MaxVotes: 5000, MaxDuration: 30},
		Features:     db.Features{TwoFaSms: 50, TwoFaEmail: 100},
		VotingTypes:  db.VotingTypes{Single: true},
	}
	c.Assert(testDB.SetPlan(integratorPlan), qt.IsNil)
	defer func() { _ = testDB.DelPlan(&db.Plan{ID: integratorPlan.ID}) }()
	c.Assert(testDB.SetOrganizationSubscription(integratorAddr, &db.OrganizationSubscription{
		PlanID:          integratorPlan.ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// create managed orgs up to the cap
	var firstManaged common.Address
	for i := 0; i < 2; i++ {
		reqBody := &apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{
				Type:    string(db.CompanyType),
				Website: fmt.Sprintf("https://managed-%d.example", i),
			},
		}
		created := requestAndParse[apicommon.OrganizationInfo](
			t, http.MethodPost, token, reqBody, "integrator", "organizations",
		)
		c.Assert(created.Address, qt.Not(qt.Equals), common.Address{})
		if i == 0 {
			firstManaged = created.Address
		}
	}
	// the third is rejected
	_, code = testRequest(t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://managed-3.example"},
		},
		"integrator", "organizations")
	c.Assert(code, qt.Equals, http.StatusBadRequest) // ErrMaxManagedOrgsReached

	// seed shared-pool usage on a managed org: 3 votes, 2 SMS, 1 email
	for i := 0; i < 3; i++ {
		c.Assert(testDB.IncrementOrganizationSentVotesCounter(firstManaged), qt.IsNil)
	}
	for i := 0; i < 2; i++ {
		c.Assert(testDB.IncrementOrganizationSentSMSCounter(firstManaged), qt.IsNil)
	}
	c.Assert(testDB.IncrementOrganizationSentEmailsCounter(firstManaged), qt.IsNil)

	// integrator info reflects usage + limits, including the plan's pooled caps and the
	// vote/SMS/email usage aggregated across the integrator's managed orgs
	info := requestAndParse[apicommon.IntegratorInfoResponse](
		t, http.MethodGet, token, nil, "integrator",
	)
	c.Assert(info.Enabled, qt.IsTrue)
	c.Assert(info.Limits.MaxManagedOrgs, qt.Equals, 2)
	c.Assert(info.Limits.MaxManagedProcesses, qt.Equals, 1)
	c.Assert(info.Limits.MaxVotes, qt.Equals, 5000)
	c.Assert(info.Limits.MaxSMS, qt.Equals, 50)
	c.Assert(info.Limits.MaxEmails, qt.Equals, 100)
	c.Assert(info.Usage.ManagedOrgs, qt.Equals, 2)
	c.Assert(info.Usage.SentVotes, qt.Equals, 3)
	c.Assert(info.Usage.SentSMS, qt.Equals, 2)
	c.Assert(info.Usage.SentEmails, qt.Equals, 1)

	// managed list returns the two orgs
	list := requestAndParse[apicommon.ListManagedOrganizations](
		t, http.MethodGet, token, nil, "integrator", "organizations",
	)
	c.Assert(list.Organizations, qt.HasLen, 2)

	// give the managed org a process-capable plan
	setOrganizationSubscription(t, firstManaged, mockPremiumPlan.ID)

	// publish under the managed org: a non-test-sized census (> TestMaxCensusSize voters) draws a
	// slot from the integrator's aggregate process quota
	members := memberIDs(postOrgMembers(t, token, firstManaged, newOrgMembers(db.TestMaxCensusSize+1)...))
	census := func(ids []string) apicommon.CensusSpec {
		return apicommon.CensusSpec{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}, MemberIDs: ids}
	}
	// it is priced, so the integrator wallet that funds its managed orgs' processes pays for it
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_integrator_managed_orgs",
	}), qt.IsNil)
	publishCensusProcess(t, token, firstManaged, census(members), 1)
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 1)

	// a second one is blocked by the aggregate quota (plan MaxProcesses == 1) at the publish
	// preflight, before any reservation, so the counter is untouched
	req := minimalVotingProcessRequest(firstManaged)
	req.StartDate = ""
	req.Census = census(members)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	resp, code := testRequest(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(string(resp), qt.Contains, errors.ErrIntegratorQuotaExceeded.Err.Error())
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 1)

	// a test-sized census is exempt: it publishes with the pool full and does not consume it
	publishCensusProcess(t, token, firstManaged, census(members[:1]), 1)
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 1)
}

// TestIntegratorTopLevelOrgCanOwnProcesses pins the rule pay-per-process introduced: an
// integrator top-level org (integrator-enabled, not itself managed) may own a /processes draft.
// It used to be refused so its elections could not bypass the managed-org pool quotas; each
// process now pays for itself, so there is no pool to bypass.
func TestIntegratorTopLevelOrgCanOwnProcesses(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token := testCreateUser(t, "integratorpass123")
	integratorAddr := testCreateOrganization(t, token)

	// enable integrator (override) and subscribe it to a plan that lets it create drafts.
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	plan := &db.Plan{
		ID:           "prod_test_toplevel_guard",
		Name:         "Integrator Top-Level Guard",
		Organization: db.PlanLimits{MaxDrafts: 10, MaxProcesses: 5, MaxCensus: 1000, MaxVotes: 5000, MaxDuration: 30},
		Features:     db.Features{TwoFaSms: 50, TwoFaEmail: 100},
	}
	c.Assert(testDB.SetPlan(plan), qt.IsNil)
	defer func() { _ = testDB.DelPlan(&db.Plan{ID: plan.ID}) }()
	c.Assert(testDB.SetOrganizationSubscription(integratorAddr, &db.OrganizationSubscription{
		PlanID: plan.ID, StartDate: time.Now(), RenewalDate: time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(), Active: true,
	}), qt.IsNil)

	// /processes draft-create on the integrator top-level org — allowed.
	_, code := testRequest(t, http.MethodPost, token, &apicommon.CreateVotingProcessRequest{
		OrgAddress: integratorAddr.Bytes(),
		Title:      db.MultiLangString{"default": "Top-level process"},
		Questions: []apicommon.VotingProcessQuestionRequest{{
			Title: db.MultiLangString{"default": "Q1"},
			Type:  db.VotingTypeSingleChoice,
		}},
	}, "processes")
	c.Assert(code, qt.Equals, http.StatusOK)
}

// TestManagedOrgOnIntegratorPlanCanCreateDraft guards the regression window the since-lifted
// top-level guard opened: managed orgs (managedBy != 0) whose plan happens to carry IntegratorLimits > 0
// — as the default plan does (scripts/defaultplan/main.go seeds MaxManagedOrgs=10, and every
// managed org gets subscribed to the default plan by createManagedOrganizationHandler) — must
// still be able to create drafts. IsIntegrator(managedOrg) is true in that case (the plan grants it),
// but the org is a leaf inside the pool, not the shell that owns it.
func TestManagedOrgOnIntegratorPlanCanCreateDraft(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token := testCreateUser(t, "integratorpass123")
	integratorAddr := testCreateOrganization(t, token)

	// enable the integrator and build a plan that also grants integrator limits — the same
	// shape the default plan uses in production.
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 5}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)
	plan := &db.Plan{
		ID:               "prod_test_default_shape",
		Name:             "Default-Shape Plan",
		Organization:     db.PlanLimits{MaxDrafts: 10, MaxProcesses: 5, MaxCensus: 1000, MaxVotes: 5000, MaxDuration: 30},
		Features:         db.Features{TwoFaSms: 50, TwoFaEmail: 100},
		IntegratorLimits: db.IntegratorLimits{MaxManagedOrgs: 10}, // the exact defaultplan shape
	}
	c.Assert(testDB.SetPlan(plan), qt.IsNil)
	c.Assert(testDB.SetOrganizationSubscription(integratorAddr, &db.OrganizationSubscription{
		PlanID: plan.ID, StartDate: time.Now(), RenewalDate: time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(), Active: true,
	}), qt.IsNil)

	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://managed.example"},
		},
		"integrator", "organizations",
	)
	// createManagedOrganizationHandler subscribes managed orgs to the default plan; the test
	// fixtures' default plan carries no integrator limits, so move the managed org onto the
	// integrator-limits-bearing plan to mirror production. IsIntegrator(managed) is then true,
	// yet ManagedBy!=0 makes it a leaf, not the top-level shell.
	c.Assert(testDB.SetOrganizationSubscription(managed.Address, &db.OrganizationSubscription{
		PlanID: plan.ID, StartDate: time.Now(), RenewalDate: time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(), Active: true,
	}), qt.IsNil)
	managedOrg, err := testDB.Organization(managed.Address)
	c.Assert(err, qt.IsNil)
	c.Assert(managedOrg.ManagedBy, qt.Not(qt.Equals), common.Address{})
	c.Assert(managedOrg.Subscription.PlanID, qt.Equals, plan.ID)

	// /processes draft-create — must succeed, not 403.
	_, code := testRequest(t, http.MethodPost, token, &apicommon.CreateVotingProcessRequest{
		OrgAddress: managed.Address.Bytes(),
		Title:      db.MultiLangString{"default": "Managed draft"},
		Questions: []apicommon.VotingProcessQuestionRequest{{
			Title: db.MultiLangString{"default": "Q1"},
			Type:  db.VotingTypeSingleChoice,
			Choices: []db.Choice{
				{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
				{Title: db.MultiLangString{"default": "No"}, Value: 1},
			},
		}},
	}, "processes")
	c.Assert(code, qt.Equals, http.StatusOK)
}
