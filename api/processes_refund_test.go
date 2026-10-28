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
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestDeleteDraftPaymentStates: what a delete does to the payment depends on its state. An open
// top-up session that cannot be expired would stay payable, so the delete refuses; one Stripe
// already expired is not expired again; a consumed top-up (its money is in the wallet) needs no
// gateway at all; and a refunded one (an earlier delete credited it back, then failed to drop the
// draft) is dropped with the draft, crediting nothing again: the ledger is the record.
func TestDeleteDraftPaymentStates(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "deletestates1234")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}
	openCheckout := func() (string, bson.ObjectID, string) {
		pid := newPricedVotingProcess(t, token, orgAddress)
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
			t, http.MethodPost, token, checkoutReq, "processes", pid, "checkout")
		return pid, oid, checkout.SessionID
	}

	// the open session cannot be expired: the draft and its payment stay
	pid, oid, _ := openCheckout()
	fake.expireErr = fmt.Errorf("stripe unreachable")
	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, token, nil, "processes", pid)
	fake.expireErr = nil
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)

	// a session Stripe already expired is not expired a second time
	pid, oid, sessionID := openCheckout()
	fake.setSessionStatus(sessionID, stripe.SessionStatusExpired)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(fake.expired, qt.Not(qt.Contains), sessionID)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// the expiry webhook of the session the delete expires drops the pending row first: the
	// delete still goes through instead of asking for a retry
	pid, oid, _ = openCheckout()
	fake.expireFn = func(string) {
		c.Check(testDB.DeleteProcessPayment(oid), qt.IsNil)
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a consumed top-up is deleted with the draft even with no gateway configured
	pid, oid, sessionID = openCheckout()
	won, err := testDB.ConsumeProcessPaymentTopUp(oid, sessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	testAPI.paymentGW = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	testAPI.paymentGW = fake
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a refunded draft that survived its delete: the retry deletes the draft and its row, and
	// credits nothing a second time
	pid = newPricedVotingProcess(t, token, orgAddress)
	oid = objectID(c, pid)
	paidCents := markProcessPaid(t, pid)
	creditRefund(t, orgAddress, oid, paidCents)
	before, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	after, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after.BalanceCents, qt.Equals, before.BalanceCents)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}

// creditRefund is the credit a draft's delete makes before it drops the draft: what it paid back
// to its organization's wallet, under the refund key.
func creditRefund(t *testing.T, orgAddress common.Address, oid bson.ObjectID, cents pricing.Cents) {
	t.Helper()
	qt.Assert(t, testDB.CreditWallet(db.WalletCredit{
		OrgAddress: orgAddress, AmountCents: cents, IdempotencyKey: db.RefundCreditKey(oid),
		Kind: db.WalletEntryCreditBack, ProcessID: oid, ProcessOrgAddress: orgAddress,
	}), qt.IsNil)
}

// failMoneyStepOnce makes the next money path of processID stop at step, once, as a crash there would.
func failMoneyStepOnce(t *testing.T, step moneyStep, processID bson.ObjectID) {
	t.Helper()
	failed := false
	moneyStepHook = func(s moneyStep, id bson.ObjectID) error {
		if s == step && id == processID && !failed {
			failed = true
			return fmt.Errorf("simulated crash at step %d", step)
		}
		return nil
	}
	t.Cleanup(func() { moneyStepHook = nil })
}

// TestDeleteRetryReleasesBranding: the branding add-on is released after the credit-back, so a
// delete whose release failed is retried — the credit not repeated, the release run again from what
// the credit-back row owes — instead of the organization keeping branding it got its money back for.
func TestDeleteRetryReleasesBranding(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "releaseretry1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newBrandedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	// paid from the wallet, which stamps the organization branding-paid
	paidCents := markProcessPaid(t, pid)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	// the first delete credits and stops before the release
	failMoneyStepOnce(t, moneyStepRelease, oid)
	requestAndAssertError(errors.ErrGenericInternalServerError, t, http.MethodDelete, adminToken, nil, "processes", pid)
	assertWallet(t, orgAddress, paidCents, db.WalletEntryCreditBack, 1)
	org, err = testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", pid)
	org, err = testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
	assertWallet(t, orgAddress, paidCents, db.WalletEntryCreditBack, 1)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}

// TestDeleteWalletPaidDraftCreditsIntegratorWallet is the managed side of the refund: a
// managed organization pays from its integrator's wallet, so a draft it abandons puts the
// money back there rather than issuing a card refund the integrator never made. Reaching a
// paid-but-unpublished managed draft is what happens when the debit lands and publication
// then fails preflight.
func TestDeleteWalletPaidDraftCreditsIntegratorWallet(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletrefund1234")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_refund")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	req := newVotingProcessRequest(managedAddr, memberIDs(members))
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_wallet_refund_topup",
	}), qt.IsNil)

	// the publish path's debit, without the publish: 26 voters without 2FA is €17.16
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, ProcessOrgAddress: managedAddr,
		AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", created.ProcessID)

	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// the credit is a creditBack in the history, not a top-up: an integrator reconciling its own
	// books has to be able to tell money it added from money it got back, and for which org
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(pricedProcessCents))
	c.Assert(entries[0].ProcessID, qt.Equals, oid)
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, managedAddr)
}

// TestDeleteWalletPaidDraftWaitsForHeadroomCharge: a delete and a census headroom charge on the
// same payer run one at a time under the integrator's wallet lock, and the delete reads what the
// draft paid off the ledger inside it: a charge that lands first is credited back with the rest,
// so the balance comes back by exactly the publish and the headroom, never short of the headroom.
func TestDeleteWalletPaidDraftWaitsForHeadroomCharge(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "walletraceref123")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_refund_race")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	req := newVotingProcessRequest(managedAddr, memberIDs(members))
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_wallet_race_topup",
	}), qt.IsNil)
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, ProcessOrgAddress: managedAddr,
		AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)

	// a headroom charge holds the wallet lock and debits while the delete waits on it
	walletLock := testAPI.walletLocks.lock(integratorAddr)
	deleted := make(chan int, 1)
	go func() {
		_, code := testRequest(t, http.MethodDelete, token, nil, "processes", created.ProcessID)
		deleted <- code
	}()
	time.Sleep(300 * time.Millisecond)
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, ProcessOrgAddress: managedAddr,
		AmountCents: 500, PriceCents: pricedProcessCents + 500,
	}), qt.IsNil)
	walletLock.Unlock()

	c.Assert(<-deleted, qt.Equals, http.StatusOK)
	assertWallet(t, integratorAddr, 100_000, db.WalletEntryCreditBack, 1)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, db.RefundCreditKey(oid))
	c.Assert(entries[0].AmountCents, qt.Equals, pricedProcessCents+500)

	// a charge after the delete's credit finds the draft refunded (or gone): nothing is debited
	vp := &db.VotingProcess{ID: oid, OrgAddress: managedAddr}
	org, err := testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	_, err = testAPI.chargeProcessWallet(processCharge{vp: vp, org: org, quote: pricing.Quote{TotalCents: 99_999}})
	c.Assert(err, qt.ErrorIs, errors.ErrPaymentSessionConflict)
	assertWallet(t, integratorAddr, 100_000, db.WalletEntryCreditBack, 1)
}

// TestRefundedDraftIsDeleteOnly covers a delete that refunded the draft and then failed to drop
// it. Publishing what is left must be refused: the integrator already has its money back, and
// the debit is keyed per (process, price), so a retry would pass without charging.
func TestRefundedDraftIsDeleteOnly(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "refundedzombie12")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_refunded_zombie")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	req := newVotingProcessRequest(managedAddr, memberIDs(members))
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_refunded_zombie_topup",
	}), qt.IsNil)

	// paid from the wallet, then the half of a delete that returns the money
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, ProcessOrgAddress: managedAddr,
		AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: pricedProcessCents, IdempotencyKey: db.RefundCreditKey(oid),
		Kind: db.WalletEntryCreditBack, ProcessID: oid, ProcessOrgAddress: managedAddr,
	}), qt.IsNil)
	_, before, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)

	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, nil,
		"processes", created.ProcessID, "publish")

	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
	_, after, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(after, qt.HasLen, len(before))
	got, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Published, qt.IsFalse)

	// finishing the delete is what it is left to do
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", created.ProcessID)
}

// TestDeletePaidDraftCreditsStandardWallet: a standard organization's paid draft goes back to
// the organization's own wallet as a creditBack — never to a card — less the branding a sibling
// already relies on, which stays the organization's.
func TestDeletePaidDraftCreditsStandardWallet(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "creditbackstd123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	paid := newBrandedVotingProcess(t, token, orgAddress)
	paidCents := markProcessPaid(t, paid)
	c.Assert(paidCents, qt.Equals, pricedProcessCents+pricing.BrandingCents)
	oid := objectID(c, paid)
	// a sibling paid on the strength of the stamp: it relies on the branding
	sibling := newBrandedVotingProcess(t, token, orgAddress)
	c.Assert(markProcessPaid(t, sibling), qt.Equals, pricedProcessCents)
	before, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", paid)
	c.Assert(fake.created, qt.HasLen, 0) // no Stripe call of any kind

	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, before.BalanceCents+pricedProcessCents)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 1)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(entries[0].ProcessID, qt.Equals, oid)
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, orgAddress)
	c.Assert(entries[0].OrgAddress, qt.Equals, orgAddress)
	c.Assert(entries[0].WithheldCents, qt.Equals, pricing.BrandingCents)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse) // withheld: still the organization's

	// a retried delete of the gone draft credits nothing twice
	requestAndAssertCode(http.StatusNotFound, t, http.MethodDelete, token, nil, "processes", paid)
	again, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(again.BalanceCents, qt.Equals, wallet.BalanceCents)
}

// TestDeletePaidDraftWithholdsBrandingOfDraftBeingPublished: a branded draft holding the publish
// claim relies on the organization's branding before it has any payment of its own (a free one
// gets its record only once published), so deleting the draft that paid for the branding withholds
// it: the €149 is not credited back and the stamp stays.
func TestDeletePaidDraftWithholdsBrandingOfDraftBeingPublished(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "withholdpublish1")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	paid := newBrandedVotingProcess(t, token, orgAddress)
	c.Assert(markProcessPaid(t, paid), qt.Equals, pricedProcessCents+pricing.BrandingCents)
	sibling := newBrandedVotingProcess(t, token, orgAddress)
	_, claimed, err := testDB.ClaimVotingProcessForPublish(objectID(c, sibling), time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	before, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", paid)
	after, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after.BalanceCents, qt.Equals, before.BalanceCents+pricedProcessCents)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
	// the credit-back row says what it kept back, and why
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].WithheldCents, qt.Equals, pricing.BrandingCents)
	c.Assert(entries[0].WithheldAddOns, qt.DeepEquals, []db.OrgAddOn{db.OrgAddOnBranding})
}

// TestDeleteRefundAndSiblingPublishShareWalletLock: a delete refunding the draft that paid the
// organization's branding races a sibling's publish debit. The refund decides what to withhold and
// releases the stamp under the payer's wallet lock, and the debit prices under it, so the sibling
// can never be priced on a stamp the refund then releases: either it pays the branding itself, or
// the refund withholds it. Before the fix the debit could price on the stamp read while it waited.
func TestDeleteRefundAndSiblingPublishShareWalletLock(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "refundracepub123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	paid := newBrandedVotingProcess(t, token, orgAddress)
	paidCents := markProcessPaid(t, paid)
	c.Assert(paidCents, qt.Equals, pricedProcessCents+pricing.BrandingCents)
	sibling := newBrandedVotingProcess(t, token, orgAddress)
	vp, err := testDB.VotingProcess(objectID(c, sibling))
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
	fundWallet(t, orgAddress, pricedProcessCents+pricing.BrandingCents)

	walletLock := testAPI.walletLocks.lock(orgAddress)
	deleted := make(chan int, 1)
	go func() {
		_, code := testRequest(t, http.MethodDelete, token, nil, "processes", paid)
		deleted <- code
	}()
	time.Sleep(300 * time.Millisecond)
	debited := make(chan error, 1)
	go func() { debited <- testAPI.debitProcessWallet(vp, org, nil) }()
	time.Sleep(300 * time.Millisecond)
	walletLock.Unlock()
	c.Assert(<-deleted, qt.Equals, http.StatusOK)
	c.Assert(<-debited, qt.IsNil)

	var refund *db.WalletLedgerEntry
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	for i := range entries {
		if entries[i].IdempotencyKey == db.RefundCreditKey(objectID(c, paid)) {
			refund = &entries[i]
		}
	}
	c.Assert(refund, qt.Not(qt.IsNil))
	siblingPaid, err := testDB.ProcessLedgerState(orgAddress, vp.ID)
	c.Assert(err, qt.IsNil)
	withheld := refund.WithheldCents == pricing.BrandingCents
	// which went first is up to the scheduler; either way the branding is never had for free
	c.Assert(siblingPaid.Charged(db.OrgAddOnBranding) || withheld, qt.IsTrue)
}

// TestDeleteDraftCreditsBackDebitWithOpenCheckout: what a draft paid is read off the wallet ledger,
// whatever its payment row says. A debit whose path stopped right after it (no stamp, no row), with
// a top-up checkout of the draft still open, is credited back by the delete — the open session
// expired, one refund row, the balance whole — and the row goes with the draft.
func TestDeleteDraftCreditsBackDebitWithOpenCheckout(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "lostdebitdelete1")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	fundWallet(t, orgAddress, pricedProcessCents)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	// the charge stops right after its debit
	failMoneyStepOnce(t, moneyStepStamp, oid)
	_, err = testAPI.chargeProcessWallet(processCharge{vp: vp, org: org, quote: pricing.Quote{TotalCents: pricedProcessCents}})
	c.Assert(err, qt.ErrorMatches, "simulated crash.*")
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	// a paid draft opens no checkout
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}, "processes", pid, "checkout")
	// an open session left from before the debit
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: orgAddress, CheckoutSessionID: "cs_open_over_debit",
		AmountCents: pricedProcessCents, TopUpCents: pricedProcessCents, Currency: "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	fake.setSessionStatus("cs_open_over_debit", stripe.SessionStatusOpen)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(fake.expired, qt.Contains, "cs_open_over_debit")
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryCreditBack, 1)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, db.RefundCreditKey(oid))
	c.Assert(entries[0].AmountCents, qt.Equals, pricedProcessCents)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}
