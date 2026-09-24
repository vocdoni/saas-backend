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

// TestVotingProcessPublishPaymentGate: a priced process cannot publish unpaid (402 with
// the quote in the payload, and the validation dry-run reports it), a paid one can still be
// edited (that is how a draft stranded by publish preflight is repaired) but not deleted,
// and publishes.
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

	// a paid draft is still editable: the publish gate re-prices whatever the edit
	// produced, so an edit can only repair the draft, never under-charge it
	update := newVotingProcessRequest(orgAddress, memberIDs(members))
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, update, "processes", pid)
	// deleting it is still refused — that would destroy what was paid for
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
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
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

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
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

// TestPublishPaidProcessRefusesGrownCensus: the price is the census size at payment time,
// but a paid draft's census can still be grown through POST /census/{id}, which has no
// payment state to consult. The publish gate is the only choke point that sees both the
// money and the current draft, so it has to re-price and refuse — otherwise the extra
// voters ride on the smaller price.
func TestPublishPaidProcessRefusesGrownCensus(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "grownpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, "users", "me")

	// pay the draft at its real quote hash: a placeholder hash would exercise the
	// mismatch path instead of the price-grew one
	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_grown_census",
		QuoteHash:         pricing.QuoteHash(input, quote.TotalCents),
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
		RequestedBy:       me.Email,
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, "cs_grown_census")
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	// grow the census behind the paid draft, through the endpoint that knows nothing
	// about payments
	all := postOrgMembers(t, token, orgAddress, newOrgMembers(45)[15:]...)
	// not postCensusParticipants: it asserts every id was added, and the org listing
	// includes the 15 already in the census
	added := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: memberIDs(all)},
		censusEndpoint, vp.CensusID.Hex())
	c.Assert(added.Added, qt.Equals, uint32(30))
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", pid, "price")
	c.Assert(price.TotalCents > quote.TotalCents, qt.IsTrue)

	// publication is refused with the new price rather than minting a 45-voter election
	// for the 15-voter price
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPost, token, nil,
		"processes", pid, "publish")
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
	c.Assert(vp.PublishInProgress(), qt.IsFalse) // no claim left behind

	// the webhook fulfillment hook is the other entry point into publication, and Stripe
	// retries an event for days — so it re-prices too, instead of publishing on the
	// strength of the paid status alone
	testAPI.publishPaidProcess(oid)
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
	c.Assert(vp.PublishInProgress(), qt.IsFalse)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid) // still paid: publish it manually
}

// TestManagedWalletDebitRepricesGrownCensus: the wallet debit is priced at the moment of
// the attempt, not fixed by the first one. A publish that fails after the debit, followed
// by census growth through POST /census/{id}, must top up the difference on the retry —
// the integrator otherwise gets the grown election for the smaller price.
func TestManagedWalletDebitRepricesGrownCensus(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletgrowth1234")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_growth")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(integratorAddr, 100_000, "cs_wallet_growth_topup"), qt.IsNil)

	// the debit the publish path performs (15 voters, email 2FA)
	c.Assert(testAPI.debitManagedProcessWallet(vp, org), qt.IsNil)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_515))

	// a retry of the unchanged draft charges nothing
	c.Assert(testAPI.debitManagedProcessWallet(vp, org), qt.IsNil)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_515))

	// grow the census behind the paid process, then retry the publish debit
	all := postOrgMembers(t, token, managedAddr, newOrgMembers(45)[15:]...)
	added := requestAndParse[apicommon.AddMembersResponse](t, http.MethodPost, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: memberIDs(all)},
		censusEndpoint, vp.CensusID.Hex())
	c.Assert(added.Added, qt.Equals, uint32(30))
	grown, _, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	c.Assert(grown.TotalCents > 1_515, qt.IsTrue)

	c.Assert(testAPI.debitManagedProcessWallet(vp, org), qt.IsNil)
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
	c.Assert(entries[0].AmountCents, qt.Equals, -(grown.TotalCents - 1_515))
	c.Assert(entries[1].AmountCents, qt.Equals, int64(-1_515))

	// and a further attempt at the unchanged price adds nothing
	c.Assert(testAPI.debitManagedProcessWallet(vp, org), qt.IsNil)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grown.TotalCents)
}

// TestPublishedCensusGrowthWithinPaidPrice: PUT /processes/{processId}/census is the only
// way a published process's census can grow, and pay-per-process prices by census size — so
// the guard has to answer the price question. Refusing every paid process would refuse every
// published non-free one, which is the feature removed. Growth that the €5 rounding absorbs
// is free and allowed; growth that raises the price is refused with the projected quote.
func TestPublishedCensusGrowthWithinPaidPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "censusgrowth123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(25)...)
	ids := memberIDs(members)

	// no 2FA channel, so only the base price is billed and the rounding leaves room: 14 to
	// 19 voters all cost €15, the 20th costs €20
	req := newVotingProcessRequest(orgAddress, ids[:15])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	c.Assert(quote.TotalCents, qt.Equals, int64(1_500))

	// pay it and publish, which is the state every priced published process is in
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_census_growth",
		QuoteHash:         pricing.QuoteHash(input, quote.TotalCents),
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, "cs_census_growth")
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// 4 more voters still cost €15: allowed, and they land in the census (202: the
	// whole-census questions need their on-chain maxCensusSize raised)
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[15:19]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(4))

	// the ones that raise the price to €20 are refused, and nothing is added
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(19))
}
