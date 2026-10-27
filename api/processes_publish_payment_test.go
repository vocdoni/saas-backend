package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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

// publishPaymentRefusal is the 402 (40178) a standard organization's publish gets when its wallet
// falls short.
type publishPaymentRefusal struct {
	Code int                              `json:"code"`
	Data apicommon.ProcessPaymentRequired `json:"data"`
}

// TestVotingProcessPublishPaymentGate: a priced process of a standard organization publishes from
// the organization's own wallet. Short, publish answers 402 with the quote, the balance and the
// shortfall (and the dry-run reports it); an open top-up checkout answers 409; covered, publish
// debits the price once — a paid draft stays editable and republishing it is free.
func TestVotingProcessPublishPaymentGate(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "gatepassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(pricedProcessVoters)...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)),
		processesCreateEndpoint).ProcessID
	oid := objectID(c, pid)

	// the dry-run lists the missing payment among the problems
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(validation.Valid, qt.IsFalse)
	c.Assert(strings.Join(validation.Errors, "; "), qt.Contains, "requires payment")

	// short: 402 with what is missing, and nothing debited
	const balance = 300
	fundWallet(t, orgAddress, balance)
	refusal := requestAndParseWithAssertCode[publishPaymentRefusal](http.StatusPaymentRequired,
		t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(refusal.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	c.Assert(refusal.Data.TotalCents, qt.Equals, pricedProcessCents)
	c.Assert(refusal.Data.BalanceCents, qt.Equals, pricing.Cents(balance))
	c.Assert(refusal.Data.ShortfallCents, qt.Equals, pricedProcessCents-balance)

	// an open top-up checkout blocks a manual publish: paying it would publish a second time
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}, "processes", pid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, pricedProcessCents-balance)
	fundWallet(t, orgAddress, pricedProcessCents)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, nil,
		"processes", pid, "publish")
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid, "checkout")
	c.Assert(fake.expired, qt.DeepEquals, []string{checkout.SessionID})

	// a draft is still editable: the publish gate re-prices whatever the edit produced
	update := newVotingProcessRequest(orgAddress, memberIDs(members))
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, update, "processes", pid)

	// covered: publishes end to end, debiting the price once, recorded on the payment
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(balance))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(payment.Payer, qt.Equals, orgAddress)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryPayment)
	c.Assert(entries[0].AmountCents, qt.Equals, -pricedProcessCents)
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, orgAddress)
}

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

// TestManagedProcessWalletPublish: a managed organization's process is paid from its
// integrator's wallet at publish time — refused (claim released) while the balance is
// short, debited exactly once when it covers the price, kept across re-publishes.
func TestManagedProcessWalletPublish(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletpassword123")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_integrator")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	pid := created.ProcessID

	// managed orgs have no wallet of their own and never see checkout — the integrator's pays at publish
	requestAndAssertError(errors.ErrManagedOrgHasNoWallet, t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}, "processes", pid, "checkout")

	// empty wallet: publish refused with 402 and the required amount; the publish claim
	// is released, so the refusal is retryable
	requestAndAssertError(errors.ErrInsufficientWalletBalance, t, http.MethodPost, token, nil,
		"processes", pid, "publish")
	requestAndAssertError(errors.ErrInsufficientWalletBalance, t, http.MethodPost, token, nil,
		"processes", pid, "publish")

	// top up enough (26 voters with paid integrator email 2FA = 1742 cents) and publish
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_wallet_topup_test",
	}), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)

	// debited exactly once, and the payment is recorded as wallet-paid
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)

	// a re-publish of the already-published process keeps the debit
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, "processes", pid, "publish")
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000-integratorProcessCents))
}

// TestManagedWalletPublishRefusesQuoteRequired: above the self-service limit a price is a custom
// quote. Card checkout refuses it, and so must the wallet: a funded integrator would otherwise
// publish a 50 000+ voter process at the list price.
func TestManagedWalletPublishRefusesQuoteRequired(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletquote12345")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_quote")
	// lift the plan past the self-service limit, so it is the price that refuses, not the plan
	plan, err := testDB.Plan("prod_test_wallet_quote")
	c.Assert(err, qt.IsNil)
	plan.Organization.MaxCensus = 100_000
	plan.Features.TwoFaEmail = 100_000
	c.Assert(testDB.SetPlan(plan), qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 10_000_000, IdempotencyKey: "cs_wallet_quote_topup",
	}), qt.IsNil)

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	census.Size = 50_001
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// refused, and retryable: the publish claim is released each time
	requestAndAssertError(errors.ErrQuoteRequired, t, http.MethodPost, token, nil,
		"processes", created.ProcessID, "publish")
	requestAndAssertError(errors.ErrQuoteRequired, t, http.MethodPost, token, nil,
		"processes", created.ProcessID, "publish")

	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000_000))
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.Equals, db.ErrNotFound)
}

// TestPublishPaidProcessRefusesGrownCensus: the price is the census size at payment time,
// but the census behind a paid draft can still grow, so the publish gate — the only choke
// point that sees both the money and the current draft — re-prices and refuses. Otherwise
// the extra voters ride on the smaller price.
func TestPublishPaidProcessRefusesGrownCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "grownpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	paidCents := markProcessPaid(t, pid) // the wallet is empty again

	// grow the census past what was paid; the org listing includes the 26 already in it
	all := postOrgMembers(t, token, orgAddress, newOrgMembers(3 * pricedProcessVoters)[pricedProcessVoters:]...)
	added, _, err := testDB.AddCensusParticipantsByMemberIDs(vp.CensusID.Hex(), memberIDs(all))
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, 2*pricedProcessVoters)
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", pid, "price")
	c.Assert(price.TotalCents > paidCents, qt.IsTrue)

	// publication is refused with the difference rather than minting a 78-voter election for the
	// 26-voter price
	refusal := requestAndParseWithAssertCode[publishPaymentRefusal](http.StatusPaymentRequired,
		t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(refusal.Data.ShortfallCents, qt.Equals, price.TotalCents-paidCents)
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
	c.Assert(vp.PublishInProgress(), qt.IsFalse) // no claim left behind
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, paidCents)

	// with the difference in the wallet the publish debits only that
	fundWallet(t, orgAddress, price.TotalCents-paidCents)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
}

// TestManagedWalletDebitRepricesGrownCensus: the wallet debit is priced at the moment of
// the attempt, not fixed by the first one. A publish that fails after the debit, followed
// by census growth outside the process endpoints, must top up the difference on the retry —
// the integrator otherwise gets the grown election for the smaller price.
func TestManagedWalletDebitRepricesGrownCensus(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletgrowth1234")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_growth")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_wallet_growth_topup",
	}), qt.IsNil)

	// the debit the publish path performs (26 voters, integrator email 2FA)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	// a retry of the unchanged draft charges nothing
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	// grow the census behind the paid process — past the census routes' check, as a racing
	// batch would — then retry the publish debit
	all := postOrgMembers(t, token, managedAddr, newOrgMembers(3 * pricedProcessVoters)[pricedProcessVoters:]...)
	added, _, err := testDB.AddCensusParticipantsByMemberIDs(vp.CensusID.Hex(), memberIDs(all))
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, 2*pricedProcessVoters)
	grown, _, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	c.Assert(grown.TotalCents > integratorProcessCents, qt.IsTrue)

	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grown.TotalCents)

	// the payment record carries the new price, and the ledger both legs
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, grown.TotalCents)
	total, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3)) // top-up + two debits
	c.Assert(entries[0].AmountCents, qt.Equals, -(grown.TotalCents - integratorProcessCents))
	c.Assert(entries[1].AmountCents, qt.Equals, pricing.Cents(-integratorProcessCents))

	// and a further attempt at the unchanged price adds nothing
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grown.TotalCents)
}

// TestManagedWalletConcurrentChargesPayOnce: headroom purchases racing at different prices
// must each charge only what the previous one left uncovered. Read unlocked, every one would
// charge its delta from the same base, and the wallet would pay the overlap several times.
func TestManagedWalletConcurrentChargesPayOnce(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletrace123456")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_race")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_wallet_race_topup",
	}), qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil) // 26-voter integrator price paid

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Go(func() {
			_, err := testAPI.chargeProcessWallet(processCharge{
				vp: vp, org: org, quote: pricing.Quote{TotalCents: pricing.Cents(2_000 + 100*i)},
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		c.Assert(err, qt.IsNil)
	}

	// whatever the order, the wallet paid the highest price exactly once
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000-2_900))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(2_900))
}

// TestWalletChargeRefusesOpenCheckout: a draft whose top-up checkout is still open may still be
// paid by the customer, so the wallet does not pay over it: refused before the debit.
func TestWalletChargeRefusesOpenCheckout(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletopencheck1")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}, "processes", pid, "checkout")
	fundWallet(t, orgAddress, 100_000)
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)

	_, err = testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: pricedProcessCents},
	})
	c.Assert(errors.Is(err, errors.ErrPaymentSessionConflict), qt.IsTrue, qt.Commentf("got %v", err))

	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.CheckoutSessionID, qt.Equals, checkout.SessionID)
}
