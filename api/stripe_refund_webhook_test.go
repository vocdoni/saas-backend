package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v86"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestStripeRefundFailedWebhook covers the refund that does not stick: Stripe accepted it,
// the draft was deleted on the strength of that, and the refund then failed. The process
// cannot come back, so the only correct reaction is to stop the payment looking settled and
// say loudly that a human must reconcile it.
func TestStripeRefundFailedWebhook(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        common.HexToAddress("0x000000000000000000000000000000000000beef"),
		CheckoutSessionID: "cs_refund_failed",
		AmountCents:       1_500,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(processID, "cs_refund_failed", db.ProcessCharge{PaymentIntentID: "pi_refund_failed"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunded, err := testDB.MarkProcessPaymentRefunded(processID, "re_failed", 1_500)
	c.Assert(err, qt.IsNil)
	c.Assert(refunded, qt.IsTrue)

	refundObject := func(status, rawProcessID string) map[string]any {
		return map[string]any{
			"id":       "re_failed",
			"object":   "refund",
			"amount":   1_500,
			"status":   status,
			"metadata": map[string]string{"voting_process_id": rawProcessID},
		}
	}

	// a refund still on its way changes nothing
	status := postSignedStripeEvent(t, testWebhookSecret, "evt_refund_pending",
		"charge.refund.updated", refundObject("pending", processID.Hex()))
	c.Assert(status, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)

	// one that failed puts the payment back to paid, keeping the refund id to reconcile
	status = postSignedStripeEvent(t, testWebhookSecret, "evt_refund_failed",
		"charge.refund.updated", refundObject("failed", processID.Hex()))
	c.Assert(status, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.RefundID, qt.Equals, "re_failed")

	// a refund naming no process is permanently invalid: answered 200 so Stripe stops
	status = postSignedStripeEvent(t, testWebhookSecret, "evt_refund_failed_bad",
		"charge.refund.updated", refundObject("failed", "not-an-object-id"))
	c.Assert(status, qt.Equals, http.StatusOK)
}

// TestStripeLateCensusTopUpRefunded: a census top-up paid after its draft was deleted and
// refunded bought nothing, so the webhook refunds it on the spot — under the per-intent key
// every refund of the process uses — instead of raising an envelope nobody can spend.
func TestStripeLateCensusTopUpRefunded(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	// Stripe's API, stubbed: the one call this path makes is the refund
	var refundBodies, refundKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Check(r.URL.Path, qt.Equals, "/v1/refunds")
		c.Check(r.ParseForm(), qt.IsNil)
		refundBodies = append(refundBodies, r.PostForm.Get("payment_intent"))
		refundKeys = append(refundKeys, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"re_late","object":"refund","status":"succeeded"}`))
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend,
		stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(srv.URL)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })

	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        common.HexToAddress("0x000000000000000000000000000000000000beef"),
		CheckoutSessionID: "cs_late_first",
		AmountCents:       1_500,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(processID, "cs_late_first", db.ProcessCharge{PaymentIntentID: "pi_late_first"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunded, err := testDB.MarkProcessPaymentRefunded(processID, "re_first", 1_500)
	c.Assert(err, qt.IsNil)
	c.Assert(refunded, qt.IsTrue)

	session := checkoutSessionObject("cs_late_topup", "paid", 500, map[string]string{
		stripe.MetadataKeyProcessID:         processID.Hex(),
		stripe.MetadataKeyProcessTopUpCents: "2000",
	})
	session["payment_intent"] = "pi_late_topup"
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_late_topup", "checkout.session.completed", session)
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(refundBodies, qt.DeepEquals, []string{"pi_late_topup"})
	c.Assert(refundKeys, qt.DeepEquals, []string{"refund:" + processID.Hex() + ":pi_late_topup"})

	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.AmountCents, qt.Equals, int64(1_500))
}
