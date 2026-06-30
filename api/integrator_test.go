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
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
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

	// a managed org may not publish a process whose declared census size exceeds the
	// integrator plan's MaxCensus (1000): the per-process bound applies to managed orgs even
	// though the aggregate process-count quota is what governs how many they may publish.
	overCapDraft, err := testDB.SetProcess(&db.Process{
		OrgAddress: firstManaged,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Over-cap managed election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 2000,
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
	_, code = testRequest(t, http.MethodPost, token, nil, "process", overCapDraft.Hex(), "publish")
	c.Assert(code, qt.Equals, http.StatusUnauthorized) // ErrProcessCensusSizeExceedsPlanLimit, wrapped

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

// TestIntegratorProcessQuotaViaTransactions guards the regression where the remote-signer
// /transactions path stopped enforcing the integrator's shared process quota for managed orgs.
// A managed org creating elections through /transactions must consume, and be capped by, the
// integrator's aggregate ManagedProcesses quota — exactly like /process/{id}/publish does.
func TestIntegratorProcessQuotaViaTransactions(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token := testCreateUser(t, "integratorpass123")
	integratorAddr := testCreateOrganization(t, token)

	// enable integrator (override) and subscribe it to a plan whose top-level MaxProcesses (1)
	// is the shared process cap across all of its managed orgs.
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	integratorPlan := &db.Plan{
		ID:           "prod_test_tx_quota",
		Name:         "Integrator Tx Quota",
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

	// create a managed org: its on-chain account is provisioned eagerly and the calling user
	// becomes its admin, so it can fund and sign NEW_PROCESS txs through /transactions.
	managed := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType), Website: "https://managed.example"},
		},
		"integrator", "organizations",
	)
	c.Assert(managed.Address, qt.Not(qt.Equals), common.Address{})

	// newProcessTx builds a marshalled NEW_PROCESS tx for the managed org with the given census
	// size. No overwrite/anonymous/weighted features are requested so the plan's feature gates
	// in HasTxPermission pass.
	newProcessTx := func(maxCensusSize uint64) []byte {
		tx := &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
			Txtype: models.TxType_NEW_PROCESS,
			Process: &models.Process{
				EntityId:      managed.Address.Bytes(),
				MaxCensusSize: maxCensusSize,
				Duration:      86400,
				EnvelopeType:  &models.EnvelopeType{},
				VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 1},
			},
		}}}
		b, err := proto.Marshal(tx)
		c.Assert(err, qt.IsNil)
		return b
	}
	postTx := func(payload []byte) int {
		_, code := testRequest(t, http.MethodPost, token,
			&apicommon.TransactionData{Address: managed.Address, TxPayload: payload}, "transactions")
		return code
	}
	managedProcesses := func() int {
		org, err := testDB.Organization(integratorAddr)
		c.Assert(err, qt.IsNil)
		return org.Counters.ManagedProcesses
	}

	// first non-test-sized process is signed and consumes one slot of the integrator pool.
	c.Assert(postTx(newProcessTx(100)), qt.Equals, http.StatusOK)
	c.Assert(managedProcesses(), qt.Equals, 1)

	// the second is capped by the integrator's aggregate quota (plan MaxProcesses == 1).
	// Before the fix this path enforced nothing and returned 200, leaving the counter at 0.
	c.Assert(postTx(newProcessTx(100)), qt.Equals, http.StatusBadRequest) // ErrIntegratorQuotaExceeded
	c.Assert(managedProcesses(), qt.Equals, 1)                            // reservation never taken / rolled back

	// a test-sized election (<= TestMaxCensusSize) is exempt: allowed even with the pool full,
	// and it does not consume the integrator quota.
	c.Assert(postTx(newProcessTx(uint64(db.TestMaxCensusSize))), qt.Equals, http.StatusOK)
	c.Assert(managedProcesses(), qt.Equals, 1)
}
