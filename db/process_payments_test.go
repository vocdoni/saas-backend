package db

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func newPendingPayment(processID bson.ObjectID, sessionID string) *ProcessPayment {
	return &ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        testOrgAddress,
		Payer:             testOrgAddress,
		CheckoutSessionID: sessionID,
		QuoteHash:         "hash-" + sessionID,
		AmountCents:       29_000,
		TopUpCents:        29_000,
		Currency:          "eur",
		RequestedBy:       testUserEmail,
	}
}

// walletPayment is a paid-from-the-wallet record of amountCents for processID.
func walletPayment(processID bson.ObjectID, amountCents pricing.Cents) *ProcessPayment {
	return &ProcessPayment{
		ProcessID: processID, OrgAddress: testOrgAddress, Payer: testOrgAddress,
		AmountCents: amountCents, Currency: "eur",
	}
}

// TestProcessPaymentTopUpCheckout walks a top-up checkout opened for a process: only its own
// session consumes it, once; an open one is never overwritten by a wallet payment, a consumed one
// is; and an expiry drops only the session it names.
func TestProcessPaymentTopUpCheckout(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	_, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_1"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// an open checkout is not the wallet's to pay over: the customer may still pay it
	ok, err = testDB.SetProcessPaymentPaidByWallet(walletPayment(processID, 29_000))
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// a stale session neither consumes nor drops it
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_other")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.DeleteProcessPaymentCheckout(processID, "cs_other")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// its own session consumes it exactly once
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsTrue)

	// consumed: the wallet pays over it, keeping the session that funded it
	paid := walletPayment(processID, 29_000)
	paid.CheckoutSessionID = "cs_1"
	ok, err = testDB.SetProcessPaymentPaidByWallet(paid)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_1")
	c.Assert(payment.Consumed, qt.IsFalse)
	c.Assert(payment.Payer, qt.Equals, testOrgAddress)

	// paid: a replayed session consumes nothing, an expiry drops nothing, no checkout replaces it
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.DeleteProcessPaymentCheckout(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_2"), "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// an expired checkout goes, and only the one named
	expiredID := bson.NewObjectID()
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(expiredID, "cs_3"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.DeleteProcessPaymentCheckout(expiredID, "cs_3")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	_, err = testDB.ProcessPayment(expiredID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}

// TestProcessPaymentTransitions walks the payment state machine and asserts every illegal
// transition loses its CAS instead of applying.
func TestProcessPaymentTransitions(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()

	// no payment yet
	_, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)

	// first checkout creates the pending payment
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_1"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPending)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_1")

	// a replacement session while still pending is allowed (obsolete session replaced)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_2"), "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// transitions demand the stored session id: the stale one loses its CAS
	ok, err = testDB.MarkProcessPaymentPaid(processID, "cs_1", ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// delayed payment method: pending -> processing
	ok, err = testDB.MarkProcessPaymentProcessing(processID, "cs_2")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// while processing, no new checkout may replace the payment
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_3"), "cs_2")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// processing -> paid
	ok, err = testDB.MarkProcessPaymentPaid(processID, "cs_2", ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.PaidAt.IsZero(), qt.IsFalse)

	// paid is terminal: a duplicate webhook loses the CAS (the idempotency signal) ...
	ok, err = testDB.MarkProcessPaymentPaid(processID, "cs_2", ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	// ... a failure event cannot regress it ...
	ok, err = testDB.MarkProcessPaymentFailed(processID, "cs_2")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	// ... and no new checkout can replace it
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_4"), "cs_2")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
}

func TestProcessPaymentFailureAndRetry(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_1"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// declined payment: pending -> failed
	ok, err = testDB.MarkProcessPaymentFailed(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentFailed)

	// a failed payment is payable again with a fresh session
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_2"), "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.MarkProcessPaymentPaid(processID, "cs_2", ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// an unpaid payment goes with its draft
	unpaidID := bson.NewObjectID()
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(unpaidID, "cs_3"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	deleted, err := testDB.DeleteProcessPayment(unpaidID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsTrue)
	_, err = testDB.ProcessPayment(unpaidID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}

func TestProcessPaymentPaidByWallet(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	payment := walletPayment(processID, 14_200)

	// a payment records the wallet it debited
	noPayer := *payment
	noPayer.Payer = common.Address{}
	_, err := testDB.SetProcessPaymentPaidByWallet(&noPayer)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)

	// the publish path needs no prior pending state
	ok, err := testDB.SetProcessPaymentPaidByWallet(payment)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	stored, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Status, qt.Equals, ProcessPaymentPaid)

	// a publish retry that already paid reports success, not conflict
	ok, err = testDB.SetProcessPaymentPaidByWallet(walletPayment(processID, 14_200))
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// census headroom raises it, keeping the original CreatedAt and PaidAt
	raisedPayment := walletPayment(processID, 20_000)
	raisedPayment.CreatedAt, raisedPayment.PaidAt = stored.CreatedAt, stored.PaidAt
	ok, err = testDB.SetProcessPaymentPaidByWallet(raisedPayment)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	raised, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(raised.AmountCents, qt.Equals, pricing.Cents(20_000))
	c.Assert(raised.CreatedAt.Equal(stored.CreatedAt), qt.IsTrue)
	c.Assert(raised.PaidAt.Equal(stored.PaidAt), qt.IsTrue)

	// but never down: a stale retry priced lower leaves the record alone
	ok, err = testDB.SetProcessPaymentPaidByWallet(walletPayment(processID, 14_200))
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue) // already paid, so the caller is not in conflict
	raised, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(raised.AmountCents, qt.Equals, pricing.Cents(20_000))

	// deleting the draft never removes a paid payment's record
	deleted, err := testDB.DeleteProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsFalse)
	_, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
}

// TestProcessPaymentPendingPinsReplacedSession: a pending payment is replaced only by a caller
// that observed its session, so two concurrent checkouts cannot both store.
func TestProcessPaymentPendingPinsReplacedSession(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()

	// two checkouts race, each having seen no payment at all: only one may store
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_a"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_b"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_a")

	// replacing a session that is no longer the stored one is refused just the same
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_c"), "cs_gone")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// the caller that actually reconciled cs_a replaces it
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_c"), "cs_a")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_c")
}

// TestProcessPaymentRefund covers the money-back transitions: only a paid payment refunds,
// the refund is recorded on the row that survives its deleted process, and a refund Stripe
// later fails puts the payment back to paid so nothing looks settled that is not.
func TestProcessPaymentRefund(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_1"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// nothing to refund until the money actually arrived
	ok, err = testDB.MarkProcessPaymentRefunded(processID, "re_1", 29_000)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	ok, err = testDB.MarkProcessPaymentPaid(processID, "cs_1", ProcessCharge{PaymentIntentID: "pi_1"})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.PaymentIntentID, qt.Equals, "pi_1")

	// paid -> refunded, recording what the money went back as
	ok, err = testDB.MarkProcessPaymentRefunded(processID, "re_1", 29_000)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentRefunded)
	c.Assert(payment.RefundID, qt.Equals, "re_1")

	// a delete retried after the refund landed must not refund twice
	ok, err = testDB.MarkProcessPaymentRefunded(processID, "re_2", 29_000)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// a refund that failed at Stripe is not a refund: back to paid, refund id kept as the
	// trace of what to reconcile by hand
	ok, err = testDB.MarkProcessPaymentRefundFailed(processID, "re_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.RefundID, qt.Equals, "re_1")

	// a replay of that event, and one naming a refund this payment never had, change nothing
	ok, err = testDB.MarkProcessPaymentRefundFailed(processID, "re_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.MarkProcessPaymentRefundFailed(processID, "re_other")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
}

// TestProcessPaymentRefundPlan: a delete fixes its refund once, against the envelope it read,
// and the payment is locked from then on — neither a card top-up nor a wallet top-up can raise
// what that refund returns.
func TestProcessPaymentRefundPlan(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	ok, err := testDB.SetProcessPaymentPaidByWallet(walletPayment(processID, 29_000))
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// planned against an envelope that moved: nothing is fixed
	ok, err = testDB.PlanProcessPaymentRefund(processID, 20_000, RefundWithholding{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	withheld := RefundWithholding{Cents: 14_900, AddOns: []OrgAddOn{OrgAddOnBranding}}
	ok, err = testDB.PlanProcessPaymentRefund(processID, 29_000, withheld)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	// set once: a second plan does not replace the first
	ok, err = testDB.PlanProcessPaymentRefund(processID, 29_000, RefundWithholding{})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.RefundWithheldCents, qt.Not(qt.IsNil))
	c.Assert(*payment.RefundWithheldCents, qt.Equals, pricing.Cents(14_900))
	c.Assert(payment.RefundWithheldAddOns, qt.DeepEquals, []OrgAddOn{OrgAddOnBranding})

	ok, err = testDB.RaiseProcessPaymentAmount(ProcessTopUp{
		ProcessID: processID, FromCents: 29_000, ToCents: 40_000, SessionID: "cs_topup",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.SetProcessPaymentPaidByWallet(walletPayment(processID, 40_000))
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(29_000))

	// a plan whose refund was refused for good is dropped, but only the plan the caller fixed
	ok, err = testDB.DropProcessPaymentRefundPlan(processID, 29_000, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.DropProcessPaymentRefundPlan(processID, 29_000, 14_900)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.RefundWithheldCents, qt.IsNil)
	c.Assert(payment.RefundWithheldAddOns, qt.IsNil)
}

// TestOrganizationBrandingReliedOnPendingSibling: a branded draft whose checkout is open counts as
// relying on the organization's branding, like a paid one: its pending payment was priced without
// branding and pins that price.
func TestOrganizationBrandingReliedOnPendingSibling(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.Address{0x14}
	setupVotingProcessOrg(c, org)
	branded := func() bson.ObjectID {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, AddOns: ProcessAddOns{Branding: true}})
		c.Assert(err, qt.IsNil)
		return id
	}
	paid, sibling := branded(), branded()

	relied, err := testDB.OrganizationAddOnReliedOn(org, OrgAddOnBranding, paid)
	c.Assert(err, qt.IsNil)
	c.Assert(relied, qt.IsFalse) // a branded draft nobody is paying for relies on nothing

	pending := newPendingPayment(sibling, "cs_sibling")
	pending.OrgAddress = org
	stored, err := testDB.SetProcessPaymentPending(pending, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	relied, err = testDB.OrganizationAddOnReliedOn(org, OrgAddOnBranding, paid)
	c.Assert(err, qt.IsNil)
	c.Assert(relied, qt.IsTrue)
}
