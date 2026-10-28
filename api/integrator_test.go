package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestIntegratorManagedOrgs exercises the integrator layer: a non-integrator org is rejected,
// an integrator that topped up creates managed orgs with no cap, the integrator info and managed-org list
// reflect usage, and publishing under a managed org is bounded only by the integrator wallet —
// no pooled process quota, and no plan on either side.
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

	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.Integrator = true
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	// once topped up, managed orgs are not capped (three is past what the old per-integrator limit
	// allowed); TestUnfundedIntegratorManagesOneOrg covers an integrator that never topped up
	const topUp = 100_000
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: topUp, IdempotencyKey: "cs_integrator_managed_orgs",
	}), qt.IsNil)
	const managedOrgs = 3
	var firstManaged common.Address
	for i := range managedOrgs {
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

	// seed usage on a managed org: 3 votes, 2 SMS, 1 email
	for range 3 {
		c.Assert(testDB.IncrementOrganizationSentVotesCounter(firstManaged), qt.IsNil)
	}
	for range 2 {
		c.Assert(testDB.IncrementOrganizationSentSMSCounter(firstManaged), qt.IsNil)
	}
	c.Assert(testDB.IncrementOrganizationSentEmailsCounter(firstManaged), qt.IsNil)

	// integrator info reflects the usage aggregated across the integrator's managed orgs
	info := requestAndParse[apicommon.IntegratorInfoResponse](
		t, http.MethodGet, token, nil, "integrator",
	)
	c.Assert(info.Enabled, qt.IsTrue)
	c.Assert(info.Usage, qt.DeepEquals, apicommon.IntegratorUsage{
		ManagedOrgs: managedOrgs, SentVotes: 3, SentSMS: 2, SentEmails: 1,
	})

	list := requestAndParse[apicommon.ListManagedOrganizations](
		t, http.MethodGet, token, nil, "integrator", "organizations",
	)
	c.Assert(list.Organizations, qt.HasLen, managedOrgs)

	// publish two priced processes under the managed org: both are paid from the wallet, with
	// no pooled process quota in the way
	members := memberIDs(postOrgMembers(t, token, firstManaged, newOrgMembers(db.TestMaxCensusSize+1)...))
	census := apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}, MemberIDs: members,
	}
	publishCensusProcess(t, token, firstManaged, census, 1)
	publishCensusProcess(t, token, firstManaged, census, 1)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents < topUp, qt.IsTrue)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3) // the top-up and one debit per process
}

// TestIntegratorTopLevelOrgCannotOwnProcesses pins that an integrator's own organization holds
// no processes: it publishes through managed organizations, one of which may be its own.
func TestIntegratorTopLevelOrgCannotOwnProcesses(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token := testCreateUser(t, "integratorpass123")
	integratorAddr := testCreateOrganization(t, token)
	// a draft saved before the organization became an integrator
	members := postOrgMembers(t, token, integratorAddr, newOrgMembers(1)...)
	earlier := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token,
		newVotingProcessRequest(integratorAddr, memberIDs(members)), processesCreateEndpoint)
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.Integrator = true
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)
	// publish refuses it with the typed 403, as draft creation does, not a 400
	requestAndAssertError(errors.ErrIntegratorTopLevelOrgCannotOwnProcess,
		t, http.MethodPost, token, nil, "processes", earlier.ProcessID, "publish")

	draft := func(org common.Address) *apicommon.CreateVotingProcessRequest {
		return &apicommon.CreateVotingProcessRequest{
			OrgAddress: org.Bytes(),
			Title:      db.MultiLangString{"default": "Process"},
			Questions: []apicommon.VotingProcessQuestionRequest{{
				Title: db.MultiLangString{"default": "Q1"},
				Type:  db.VotingTypeSingleChoice,
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
					{Title: db.MultiLangString{"default": "No"}, Value: 1},
				},
			}},
		}
	}
	requestAndAssertError(errors.ErrIntegratorTopLevelOrgCannotOwnProcess,
		t, http.MethodPost, token, draft(integratorAddr), "processes")

	// its managed organization can
	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://own.example"},
		},
		"integrator", "organizations",
	)
	_, code := testRequest(t, http.MethodPost, token, draft(managed.Address), "processes")
	c.Assert(code, qt.Equals, http.StatusOK)
}
