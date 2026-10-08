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

// fakeRefund is one recorded RefundProcessPayment call.
type fakeRefund struct {
	processID       bson.ObjectID
	paymentIntentID string
	amountCents     pricing.Cents
}

// RefundProcessPayment records the refund and reports it succeeded, unless the test
// installed refundFn to make Stripe fail.
func (f *fakePaymentGW) RefundProcessPayment(
	processID bson.ObjectID, paymentIntentID string, amountCents pricing.Cents,
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
	const withBrandingCents = pricing.Cents(1_515) + 14_900

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
	c.Assert(price.TotalCents, qt.Equals, pricing.Cents(1_515))

	// a census top-up is its own charge, so it has to come back too
	raised, err := testDB.RaiseProcessPaymentAmount(db.ProcessTopUp{
		ProcessID: oid, FromCents: withBrandingCents, ToCents: withBrandingCents + 500,
		SessionID: "cs_test_topup", PaymentIntentID: "pi_test_topup",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(raised, qt.IsTrue)

	// a second top-up landing while the delete refunds: the refund plan locked the envelope,
	// so it raises nothing (its own webhook refunds it on arrival) and the delete goes through
	fake.refundFn = func(processID bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		fake.refundFn = nil // called under fake.mu; only the first refund races the top-up
		raised, err := testDB.RaiseProcessPaymentAmount(db.ProcessTopUp{
			ProcessID: oid, FromCents: withBrandingCents + 500, ToCents: withBrandingCents + 900,
			SessionID: "cs_test_late", PaymentIntentID: "pi_test_late",
		})
		c.Check(err, qt.IsNil)
		c.Check(raised, qt.IsFalse)
		return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	var intents []string
	for _, refund := range fake.refunds {
		c.Assert(refund.processID, qt.Equals, oid)
		intents = append(intents, refund.paymentIntentID)
	}
	c.Assert(intents, qt.DeepEquals, []string{"pi_test_refund", "pi_test_topup"})
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
	c.Assert(checkout.AmountCents, qt.Equals, pricing.Cents(1_515+14_900))
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
	recorded, err := testDB.SetProcessPaymentEnvelope(sibling, orgAddress, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(recorded, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 1)
	c.Assert(fake.refunds[0].paymentIntentID, qt.Equals, "pi_test_withhold")
	// €149 grossed up by 19862/16415 is €180.29; the €18.33 left is the base price and its VAT
	c.Assert(fake.refunds[0].amountCents, qt.Equals, pricing.Cents(1_833))

	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

// TestDeletePaidDraftWithholdsBrandingOfPendingCheckout: a sibling whose open checkout was priced
// without branding (the stamp was set) relies on it too. Its payment pins that price, so if the
// branding were refunded and the stamp cleared, paying that session would publish it branded with
// nobody having paid the €149. The refund withholds it, as for a paid sibling.
func TestDeletePaidDraftWithholdsBrandingOfPendingCheckout(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "withholdpending12")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", paid, "checkout")
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{
		PaymentIntentID: "pi_test_withhold_pending", SubtotalCents: 16_415, TotalCents: 19_862,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// the sibling's checkout is open, priced without branding on the strength of the stamp
	sibling := newBrandedVotingProcess(t, adminToken, orgAddress)
	siblingCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", sibling, "checkout")
	c.Assert(siblingCheckout.AmountCents, qt.Equals, pricing.Cents(1_515))

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 1)
	c.Assert(fake.refunds[0].amountCents, qt.Equals, pricing.Cents(1_833))
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
}

// TestDeletePaidDraftRetryKeepsRefundPlan: a delete that refunded part of the money and then
// failed is retried with the refund the first attempt fixed. A sibling that started relying on
// the branding in between would otherwise make the retry withhold it: a partial refund under
// another key, which Stripe answers as already refunded, and the organization keeping branding
// it got its money back for.
func TestDeletePaidDraftRetryKeepsRefundPlan(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.refundFn = func(processID bson.ObjectID, intent string) (*stripe.RefundInfo, error) {
		if intent == "pi_test_plan_topup" {
			return nil, fmt.Errorf("refund declined")
		}
		return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
	}
	adminToken := testCreateUser(t, "refundplan123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	const withBrandingCents = pricing.Cents(1_515) + 14_900

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", paid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{
		PaymentIntentID: "pi_test_plan", SubtotalCents: 16_415, TotalCents: 19_862,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	raised, err := testDB.RaiseProcessPaymentAmount(db.ProcessTopUp{
		ProcessID: oid, FromCents: withBrandingCents, ToCents: withBrandingCents + 500,
		SessionID: "cs_test_plan_topup", PaymentIntentID: "pi_test_plan_topup",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(raised, qt.IsTrue)

	// nothing relies on the branding yet: the first intent comes back whole, the top-up fails
	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 2)
	c.Assert(fake.refunds[0].paymentIntentID, qt.Equals, "pi_test_plan")
	c.Assert(fake.refunds[0].amountCents, qt.Equals, pricing.Cents(0))
	// half refunded, so the draft is delete-only
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	_, err = testAPI.paymentDueForPublish(vp)
	c.Assert(errors.Is(err, errors.ErrPaymentSessionConflict), qt.IsTrue, qt.Commentf("got %v", err))

	// a sibling starts relying on the branding before the retry
	sibling, err := bson.ObjectIDFromHex(newBrandedVotingProcess(t, adminToken, orgAddress))
	c.Assert(err, qt.IsNil)
	recorded, err := testDB.SetProcessPaymentEnvelope(sibling, orgAddress, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(recorded, qt.IsTrue)

	fake.refundFn = nil
	fake.refunds = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 2)
	c.Assert(fake.refunds[0].paymentIntentID, qt.Equals, "pi_test_plan")
	c.Assert(fake.refunds[0].amountCents, qt.Equals, pricing.Cents(0)) // the same refund, the same key
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	// the branding money came back, so the branding goes with it
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
}

// TestDeletePaidDraftKeepsDraftWhenRefundFails is the failure this flow must never get
// wrong: if the money cannot go back, the draft it paid for stays. Deleting it anyway would
// destroy the process and keep the payment. The delete also holds the publish claim while it
// refunds, so a publish cannot put the draft on chain with its money going back, and it lets
// go of it when it gives up, so the delete can be retried. Its refund is fixed by then, so
// the draft is delete-only: publishing it is refused.
func TestDeletePaidDraftKeepsDraftWhenRefundFails(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	var publishClaimedMidRefund bool
	fake.refundFn = func(processID bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		_, claimed, err := testDB.ClaimVotingProcessForPublish(processID, time.Time{})
		c.Check(err, qt.IsNil)
		publishClaimedMidRefund = claimed
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

	c.Assert(publishClaimedMidRefund, qt.IsFalse)
	_, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
}

// TestDeletePaidDraftRefundRefusedForGood: a refund Stripe refuses for good (a disputed charge)
// never happens on a retry, so a plan that waits for it would strand the draft — publish refused,
// every delete refused again. With one intent nothing moved, so the plan is dropped and the
// process is a paid draft again; a failure that may clear keeps the plan, as above.
func TestDeletePaidDraftRefundRefusedForGood(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.refundFn = func(bson.ObjectID, string) (*stripe.RefundInfo, error) {
		return nil, fmt.Errorf("charge_disputed: %w", stripe.ErrRefundRefused)
	}
	adminToken := testCreateUser(t, "refundrefused1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", pid, "checkout")
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_disputed"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, adminToken, nil, "processes", pid)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.RefundWithheldCents, qt.IsNil)
	c.Assert(testAPI.refuseRefundedDraft(oid), qt.IsNil) // publishable again
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
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", pid, "checkout")
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_release"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	// what a failed first attempt leaves: the plan fixed, the payment still paid
	planned, err := testDB.PlanProcessPaymentRefund(oid, checkout.AmountCents, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(planned, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", pid)
	org, err := testDB.Organization(orgAddress)
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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(100_000))
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
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(1_500))
	c.Assert(entries[0].ProcessID, qt.Equals, oid)
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

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(15)...)
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: 1_500, PriceCents: 1_500,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, AmountCents: 1_500, Currency: "eur",
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: 500, PriceCents: 2_000,
	}), qt.IsNil)
	stored, err = testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, AmountCents: 2_000, Currency: "eur",
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

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(15)...)
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
		OrgAddress: integratorAddr, ProcessID: oid, AmountCents: 1_500, PriceCents: 1_500,
	}), qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID: oid, OrgAddress: managedAddr, AmountCents: 1_500, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	refundKey := "refund:" + oid.Hex()
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 1_500, IdempotencyKey: refundKey,
		Kind: db.WalletEntryRefund, ProcessID: oid,
	}), qt.IsNil)
	marked, err := testDB.MarkProcessPaymentRefunded(oid, refundKey, 1_500)
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
