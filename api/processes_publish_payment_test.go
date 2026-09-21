package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// pollProcessPublished polls the process read (as a manager, so the draft state is
// visible too) until it reports published.
func pollProcessPublished(t *testing.T, token, pid string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		info := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		if info.Published {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %s not published in time", pid)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestVotingProcessPublishPaymentGate: a priced process cannot publish unpaid (402 with
// the quote in the payload, and the validation dry-run reports it), a paid one is locked
// against edits and deletion, and publishes.
func TestVotingProcessPublishPaymentGate(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "gatepassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)),
		processesCreateEndpoint)
	pid := created.ProcessID
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	// the dry-run lists the missing payment among the problems
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(validation.Valid, qt.IsFalse)
	c.Assert(strings.Join(validation.Errors, "; "), qt.Contains, "requires payment")

	// publish is refused with 402 while unpaid
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPost, token, nil,
		"processes", pid, "publish")

	// pay (as the webhook would: pending session marked paid)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_gate_test",
		QuoteHash:         "hash_at_payment_time",
		AmountCents:       1_515,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, "cs_gate_test")
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// a paid draft can no longer be edited or deleted (the price was for this draft)
	update := newVotingProcessRequest(orgAddress, memberIDs(members))
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPut, token, update,
		"processes", pid)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, token, nil,
		"processes", pid)

	// paid -> publishes end to end
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)
}

// TestPublishPaidProcessServerSide exercises the webhook fulfillment hook: once a
// payment is verified, the process publishes as the user who requested the checkout,
// with no client involvement.
func TestPublishPaidProcessServerSide(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "hookpassword1234")
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, "users", "me")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)

	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_hook_test",
		QuoteHash:         "hash",
		AmountCents:       1_515,
		Currency:          "eur",
		RequestedBy:       me.Email,
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, "cs_hook_test")
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// the hook the stripe service fires after winning the paid CAS
	testAPI.publishPaidProcess(oid)
	pollProcessPublished(t, token, created.ProcessID)

	// firing it again (a replayed fulfillment) is a no-op on a published process
	testAPI.publishPaidProcess(oid)
}

// TestPublishPaidProcessRefusals: every refusal inside the fulfillment hook must leave
// the process paid and unpublished — publishing later (manually, for free) is always
// possible, and the user is never charged again.
func TestPublishPaidProcessRefusals(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "hookrefusals1234")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	payAndHook := func(pid, requestedBy, sessionID string) bson.ObjectID {
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
			ProcessID: oid, OrgAddress: orgAddress, CheckoutSessionID: sessionID,
			QuoteHash: "hash", AmountCents: 1_515, Currency: "eur", RequestedBy: requestedBy,
		}, "")
		c.Assert(err, qt.IsNil)
		c.Assert(stored, qt.IsTrue)
		won, err := testDB.MarkProcessPaymentPaid(oid, sessionID)
		c.Assert(err, qt.IsNil)
		c.Assert(won, qt.IsTrue)
		testAPI.publishPaidProcess(oid)
		return oid
	}
	assertPaidUnpublished := func(oid bson.ObjectID) {
		payment, err := testDB.ProcessPayment(oid)
		c.Assert(err, qt.IsNil)
		c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
		vp, err := testDB.VotingProcess(oid)
		c.Assert(err, qt.IsNil)
		c.Assert(vp.Published, qt.IsFalse)
		c.Assert(vp.PublishInProgress(), qt.IsFalse) // no claim left behind
	}

	// the user who paid no longer exists: stays paid, publish manually later
	ghostPid := newPricedVotingProcess(t, token, orgAddress)
	assertPaidUnpublished(payAndHook(ghostPid, "ghost@nowhere.example", "cs_refusal_ghost"))

	// the draft fails preflight at fulfillment time (a stray question corrupted the
	// stored set after payment): stays paid, no claim, repairable + publishable later
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, "users", "me")
	strayPid := newPricedVotingProcess(t, token, orgAddress)
	strayOid, err := bson.ObjectIDFromHex(strayPid)
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: strayOid, OrgAddress: orgAddress, Order: 2,
		Title:     db.MultiLangString{"default": "stray"},
		Type:      db.VotingTypeSingleChoice,
		TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
		Choices:   []db.Choice{{Title: db.MultiLangString{"default": "Yes"}, Value: 0}},
	})
	c.Assert(err, qt.IsNil)
	assertPaidUnpublished(payAndHook(strayPid, me.Email, "cs_refusal_stray"))
}

// TestManagedProcessWalletPublish: a managed organization's process is paid from its
// integrator's wallet at publish time — refused (claim released) while the balance is
// short, debited exactly once when it covers the price, kept across re-publishes.
func TestManagedProcessWalletPublish(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletpassword123")
	integratorAddr := testCreateOrganization(t, token)

	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)
	integratorPlan := &db.Plan{
		ID:           "prod_test_wallet_integrator",
		Name:         "Wallet Integrator",
		Organization: db.PlanLimits{MaxProcesses: 5, MaxCensus: 10_000, MaxDuration: 30, MaxDrafts: 5},
		VotingTypes:  db.VotingTypes{Single: true, Multiple: true},
		Features:     db.Features{TwoFaEmail: 10_000, TwoFaSms: 10_000},
	}
	c.Assert(testDB.SetPlan(integratorPlan), qt.IsNil)
	defer func() { _ = testDB.DelPlan(&db.Plan{ID: integratorPlan.ID}) }()
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

	members := postOrgMembers(t, token, managed.Address, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managed.Address, memberIDs(members)),
		processesCreateEndpoint)
	pid := created.ProcessID

	// managed orgs never see checkout — the wallet pays at publish time
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}, "processes", pid, "checkout")

	// empty wallet: publish refused with 402 and the required amount; the publish claim
	// is released, so the refusal is retryable
	requestAndAssertError(errors.ErrInsufficientWalletBalance, t, http.MethodPost, token, nil,
		"processes", pid, "publish")
	requestAndAssertError(errors.ErrInsufficientWalletBalance, t, http.MethodPost, token, nil,
		"processes", pid, "publish")

	// top up enough (15 voters email 2FA = 1515 cents) and publish
	c.Assert(testDB.CreditWallet(integratorAddr, 100_000, "cs_wallet_topup_test"), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)

	// debited exactly once, and the payment is recorded as wallet-paid
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_515))
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)

	// a re-publish of the already-published process keeps the debit
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, "processes", pid, "publish")
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_515))
}
