package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

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
// draft) is kept as the audit record.
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

	// a refunded payment whose draft survived: the retry deletes the draft, keeps the row, and
	// credits nothing a second time
	pid = newPricedVotingProcess(t, token, orgAddress)
	oid = objectID(c, pid)
	paidCents := markProcessPaid(t, pid)
	won, err = testDB.MarkProcessPaymentRefunded(oid, paidCents)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	before, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	after, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after.BalanceCents, qt.Equals, before.BalanceCents)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

// TestDeleteRetryReleasesBranding: the branding add-on is released before the payment is marked
// refunded, so a delete whose release failed is retried — the refund reused, the release run
// again — instead of the organization keeping branding it got its money back for. This drives
// that retry: the refund is planned and returned, the payment not yet marked.
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
	// what a failed first attempt leaves: the plan fixed, the payment still paid
	planned, err := testDB.PlanProcessPaymentRefund(oid, paidCents, db.RefundWithholding{})
	c.Assert(err, qt.IsNil)
	c.Assert(planned, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", pid)
	org, err = testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, Payer: integratorAddr, AmountCents: pricedProcessCents, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", created.ProcessID)

	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)

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

// TestDeleteWalletPaidDraftWaitsForHeadroomCharge: a wallet charge of census headroom debits
// before it records the new amount. A delete that planned its refund in between would leave the
// debit refused a record and refunded by nobody, so the plan waits on the integrator's wallet
// lock: the charge in flight lands first, the delete's amount check then misses (409), and its
// retry refunds the grown amount — nothing is lost.
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, Payer: integratorAddr, AmountCents: pricedProcessCents, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	// a headroom charge is in flight: it holds the wallet lock between its debit and its record
	walletLock := testAPI.walletLocks.lock(integratorAddr)
	deleted := make(chan int, 1)
	go func() {
		_, code := testRequest(t, http.MethodDelete, token, nil, "processes", created.ProcessID)
		deleted <- code
	}()
	time.Sleep(300 * time.Millisecond)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.RefundWithheldCents, qt.IsNil) // the delete has not planned past the charge
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: 500, PriceCents: pricedProcessCents + 500,
	}), qt.IsNil)
	stored, err = testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, Payer: integratorAddr, AmountCents: pricedProcessCents + 500,
		Currency:  "eur",
		CreatedAt: payment.CreatedAt, PaidAt: payment.PaidAt,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue) // the charge records: no debit is left without its record
	walletLock.Unlock()

	// the delete read the old amount, so it is told to retry; the retry returns all of it
	c.Assert(<-deleted, qt.Equals, http.StatusConflict)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", created.ProcessID)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, Payer: integratorAddr, AmountCents: pricedProcessCents, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	refundKey := "refund:" + oid.Hex()
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: pricedProcessCents, IdempotencyKey: refundKey,
		Kind: db.WalletEntryCreditBack, ProcessID: oid,
	}), qt.IsNil)
	marked, err := testDB.MarkProcessPaymentRefunded(oid, pricedProcessCents)
	c.Assert(err, qt.IsNil)
	c.Assert(marked, qt.IsTrue)
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
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.Payer, qt.Equals, orgAddress)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 1)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(entries[0].ProcessID, qt.Equals, oid)
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, orgAddress)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse) // withheld: still the organization's

	// a retried delete of the gone draft credits nothing twice
	requestAndAssertCode(http.StatusNotFound, t, http.MethodDelete, token, nil, "processes", paid)
	again, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(again.BalanceCents, qt.Equals, wallet.BalanceCents)
}
