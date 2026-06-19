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
	plainAddr := testCreateOrganization(t, token) // a normal org owned by the same user

	// rejection: a non-integrator org cannot create managed orgs
	body := &apicommon.CreateManagedOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://managed.example"},
	}
	_, code := testRequest(t, http.MethodPost, token, body, "organizations", plainAddr.String(), "managed")
	c.Assert(code, qt.Equals, http.StatusForbidden) // ErrNotAnIntegrator

	// enable integrator with an override (override beats plan):
	// MaxManagedOrgs 2, MaxManagedProcesses 1, MaxManagedCensusSize 1000
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IsIntegrator = true
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{
		MaxManagedOrgs:       2,
		MaxManagedProcesses:  1,
		MaxManagedCensusSize: 1000,
	}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

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
			t, http.MethodPost, token, reqBody, "organizations", integratorAddr.String(), "managed",
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
		"organizations", integratorAddr.String(), "managed")
	c.Assert(code, qt.Equals, http.StatusBadRequest) // ErrMaxManagedOrgsReached

	// integrator info reflects usage + limits
	info := requestAndParse[apicommon.IntegratorInfoResponse](
		t, http.MethodGet, token, nil, "organizations", integratorAddr.String(), "integrator",
	)
	c.Assert(info.Enabled, qt.IsTrue)
	c.Assert(info.Limits.MaxManagedOrgs, qt.Equals, 2)
	c.Assert(info.Usage.ManagedOrgs, qt.Equals, 2)

	// managed list returns the two orgs
	list := requestAndParse[apicommon.ListManagedOrganizations](
		t, http.MethodGet, token, nil, "organizations", integratorAddr.String(), "managed",
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
	published := requestAndParse[apicommon.PublishProcessResponse](
		t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish",
	)
	c.Assert(len(published.Address) > 0, qt.IsTrue)

	// the integrator's aggregate counters were bumped
	integratorOrg, err = testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(integratorOrg.Counters.ManagedProcesses, qt.Equals, 1)
	c.Assert(integratorOrg.Counters.ManagedCensusSize, qt.Equals, 100)

	// a second publish is blocked by the aggregate quota (MaxManagedProcesses == 1)
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
