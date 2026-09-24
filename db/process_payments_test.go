package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func newPendingPayment(processID bson.ObjectID, sessionID string) *ProcessPayment {
	return &ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        testOrgAddress,
		CheckoutSessionID: sessionID,
		QuoteHash:         "hash-" + sessionID,
		AmountCents:       29_000,
		Currency:          "eur",
		RequestedBy:       testUserEmail,
	}
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
}

func TestProcessPaymentPaidByWallet(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	payment := &ProcessPayment{
		ProcessID:   processID,
		OrgAddress:  testOrgAddress,
		AmountCents: 14_200,
		Currency:    "eur",
	}

	// the integrator publish path needs no prior pending state
	ok, err := testDB.SetProcessPaymentPaidByWallet(payment)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	stored, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(stored.CheckoutSessionID, qt.Equals, "")

	// a publish retry that already paid reports success, not conflict
	ok, err = testDB.SetProcessPaymentPaidByWallet(payment)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// a retry after the census grew tops the record up to the new price, keeping the
	// original CreatedAt so the record's history survives
	toppedUp := *payment
	toppedUp.AmountCents = 20_000
	toppedUp.CreatedAt = stored.CreatedAt
	ok, err = testDB.SetProcessPaymentPaidByWallet(&toppedUp)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	raised, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(raised.AmountCents, qt.Equals, int64(20_000))
	c.Assert(raised.CreatedAt.Equal(stored.CreatedAt), qt.IsTrue)

	// but never down: a stale retry priced lower leaves the record alone
	lowered := toppedUp
	lowered.AmountCents = 14_200
	ok, err = testDB.SetProcessPaymentPaidByWallet(&lowered)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue) // already paid, so the caller is not in conflict
	raised, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(raised.AmountCents, qt.Equals, int64(20_000))

	// deleting the draft removes its payment state
	c.Assert(testDB.DeleteProcessPayment(processID), qt.IsNil)
	_, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}

// TestProcessPaymentPendingPinsReplacedSession: a pending payment may only be replaced by
// a caller that observed the session it is replacing. Without that condition two
// simultaneous checkouts both store successfully, and the session the loser opened stays
// open and payable while the record points at the winner's — money that fulfills nothing.
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
