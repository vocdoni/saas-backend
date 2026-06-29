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

// TestIntegratorManagedOrgs exercises the integrator layer: a non-integrator org is
// rejected, an integrator can create managed orgs up to its quota, the integrator info
// and managed-org list reflect usage, and publishing under a managed org enforces the
// integrator's aggregate process/census quota.
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
	// MaxProcesses 1, MaxCensus 1000.
	integratorPlan := &db.Plan{
		ID:           "prod_test_integrator_caps",
		Name:         "Integrator Caps",
		Organization: db.PlanLimits{MaxProcesses: 1, MaxCensus: 1000, MaxVotes: 5000, MaxDuration: 30},
		Features:     db.Features{TwoFaSms: 50, TwoFaEmail: 100},
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

	// publish under a managed org: give it a process-capable plan
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(firstManaged, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// seed + publish a draft (MaxCensusSize 100, within the 1000 integrator cap)
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: firstManaged,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Managed election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 100,
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
	c.Assert(err, qt.IsNil)
	pubJob := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(pubJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", pubJob.Error))
	c.Assert(len(pubJob.Result.Address) > 0, qt.IsTrue)

	// the integrator's aggregate process counter was bumped
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 1)

	// a second publish is blocked by the aggregate quota (plan MaxProcesses == 1)
	draftID2, err := testDB.SetProcess(&db.Process{
		OrgAddress: firstManaged,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Managed election 2"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 100,
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
	c.Assert(err, qt.IsNil)
	_, code = testRequest(t, http.MethodPost, token, nil, "process", draftID2.Hex(), "publish")
	c.Assert(code, qt.Equals, http.StatusBadRequest) // ErrIntegratorQuotaExceeded
}
