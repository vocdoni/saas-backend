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
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// fakeRefund is one recorded RefundProcessPayment call.
type fakeRefund struct {
	processID       bson.ObjectID
	paymentIntentID string
	amountCents     int64
}

// RefundProcessPayment records the refund and reports it succeeded, unless the test
// installed refundFn to make Stripe fail.
func (f *fakePaymentGW) RefundProcessPayment(
	processID bson.ObjectID, paymentIntentID string, amountCents int64,
) (*stripe.RefundInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds = append(f.refunds, fakeRefund{
		processID: processID, paymentIntentID: paymentIntentID, amountCents: amountCents,
	})
	if f.refundFn != nil {
		return f.refundFn(processID, paymentIntentID)
	}
	return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
}

// TestDeleteDraftPaymentStates: what a delete does to the payment depends on its state. A
// session that cannot be expired may still be paid, so the delete refuses; one Stripe already
// expired is not expired again; a failed payment needs no gateway at all; and a refunded one
// (an earlier delete refunded, then failed to drop the draft) is kept as the audit record.
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

	// a failed payment is deleted with the draft even with no gateway configured
	pid, oid, sessionID = openCheckout()
	won, err := testDB.MarkProcessPaymentFailed(oid, sessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	testAPI.paymentGW = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	testAPI.paymentGW = fake
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a refunded payment whose draft survived: the retry deletes the draft, keeps the row
	pid, oid, sessionID = openCheckout()
	fake.setSessionStatus(sessionID, stripe.SessionStatusComplete)
	won, err = testDB.MarkProcessPaymentPaid(oid, sessionID, db.ProcessCharge{PaymentIntentID: "pi_states"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	statesPayment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	won, err = testDB.MarkProcessPaymentRefunded(oid, "re_states", statesPayment.AmountCents)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunds := len(fake.refunds)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(fake.refunds, qt.HasLen, refunds)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

// TestDeletePaidDraftRefunds covers the way out of a paid draft the organization no longer
// wants: DELETE returns the money before it removes the process. The payment document stays
// behind as the record that money came and went — the process it names is gone, so nothing
// can be paid for again — and the organization's once-only branding add-on is released with
// the refund, because it is about to be charged for it again.
func TestDeletePaidDraftRefunds(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "refundpass123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	const withBrandingCents = int64(1_515) + 14_900

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	next := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", paid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)

	// fulfillment, as the webhook performs it: the payment records the intent a refund is
	// issued against, and the organization is stamped as having paid branding
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_refund"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// the stamp spares the next draft branding, but the paid one keeps it in its price: a
	// re-quote without it would hand the €149 to census growth
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", paid, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, int64(1_515))

	// a census top-up is its own charge, so it has to come back too
	raised, err := testDB.RaiseProcessPaymentAmount(oid, withBrandingCents+500, "cs_test_topup", "pi_test_topup")
	c.Assert(err, qt.IsNil)
	c.Assert(raised, qt.IsTrue)

	// a second top-up landing while the delete refunds: its intent was not in the snapshot,
	// so the payment is not recorded as refunded and the draft survives for a retry
	fake.refundFn = func(processID bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		fake.refundFn = nil // called under fake.mu; the retry must not raise again
		_, err := testDB.RaiseProcessPaymentAmount(oid, withBrandingCents+900, "cs_test_late", "pi_test_late")
		c.Assert(err, qt.IsNil)
		return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
	}
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, adminToken, nil, "processes", paid)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)

	fake.refunds = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	var intents []string
	for _, refund := range fake.refunds {
		c.Assert(refund.processID, qt.Equals, oid)
		intents = append(intents, refund.paymentIntentID)
	}
	c.Assert(intents, qt.DeepEquals, []string{"pi_test_refund", "pi_test_topup", "pi_test_late"})
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.RefundID, qt.Equals, "re_test_"+oid.Hex())

	// branding came back with the money, so the next draft is charged for it again
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
}

// TestDeletePaidDraftWithholdsReliedOnBranding: once another process got its branding free on
// the strength of the deleted draft's payment, the €149 is in use and does not come back. The
// card refund withholds it grossed up by the VAT the charge carried, and the organization keeps
// its stamp, so the process relying on it is not re-priced.
func TestDeletePaidDraftWithholdsReliedOnBranding(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "withholdpass1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", paid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, int64(1_515+14_900))
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)
	// €164.15 net at 21% VAT
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{
		PaymentIntentID: "pi_test_withhold", SubtotalCents: 16_415, TotalCents: 19_862,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// a sibling that paid without branding because of the stamp, yet carries it
	sibling, err := bson.ObjectIDFromHex(newBrandedVotingProcess(t, adminToken, orgAddress))
	c.Assert(err, qt.IsNil)
	recorded, err := testDB.SetProcessPaymentFree(sibling, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(recorded, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 1)
	c.Assert(fake.refunds[0].paymentIntentID, qt.Equals, "pi_test_withhold")
	// €149 grossed up by 19862/16415 is €180.29; the €18.33 left is the base price and its VAT
	c.Assert(fake.refunds[0].amountCents, qt.Equals, int64(1_833))

	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

// TestDeletePaidDraftKeepsDraftWhenRefundFails is the failure this flow must never get
// wrong: if the money cannot go back, the draft it paid for stays. Deleting it anyway would
// destroy the process and keep the payment.
func TestDeletePaidDraftKeepsDraftWhenRefundFails(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.refundFn = func(_ bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		return nil, fmt.Errorf("refund declined")
	}
	adminToken := testCreateUser(t, "refundfail123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", pid, "checkout")
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_declined"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, adminToken, nil, "processes", pid)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.RefundID, qt.Equals, "")
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

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(15)...)
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

	// the publish path's debit, without the publish: 15 voters without 2FA is €15
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: 1_500, PriceCents: 1_500,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, AmountCents: 1_500, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", created.ProcessID)

	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.RefundID, qt.Equals, "refund:"+oid.Hex())

	// the credit is a refund in the ledger, not a top-up: an integrator reconciling its own
	// books has to be able to tell money it added from money it got back
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryRefund)
	c.Assert(entries[0].AmountCents, qt.Equals, int64(1_500))
	c.Assert(entries[0].ProcessID, qt.Equals, oid)
}
