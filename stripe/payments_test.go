package stripe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/pricing"
)

// TestBuildPaymentSessionParams pins the wire shape of a one-time checkout session: mode
// payment, embedded UI, tax exclusive EUR price_data, invoice creation, and the routing
// metadata on the session itself (a payment session has no SubscriptionData) and on its payment
// intent, which a refund made in the dashboard names.
func TestBuildPaymentSessionParams(t *testing.T) {
	c := qt.New(t)

	params := buildPaymentSessionParams(&PaymentSessionParams{
		LineItems: []PaymentLineItem{
			{Description: "voting process (600 eligible voters)", AmountCents: 29_000},
			{Description: "email 2FA", AmountCents: 600},
		},
		Metadata: map[string]string{
			MetadataKeyWalletTopUpOrg: "0x000000000000000000000000000000000000bEEF",
			MetadataKeyProcessID:      "656e6f7567682062797465732100",
			MetadataKeyRequestedBy:    "admin@example.com",
		},
		ReturnURL: "https://app.example.com/payment",
		Locale:    "ca", // unsupported by Stripe, must fall back to es like the subscription flow
	})

	c.Assert(*params.Mode, qt.Equals, "payment")
	c.Assert(*params.UIMode, qt.Equals, "elements")
	c.Assert(*params.AutomaticTax.Enabled, qt.IsTrue)
	c.Assert(*params.TaxIDCollection.Enabled, qt.IsTrue)
	c.Assert(*params.InvoiceCreation.Enabled, qt.IsTrue)
	c.Assert(*params.BillingAddressCollection, qt.Equals, "auto")
	c.Assert(*params.Locale, qt.Equals, "es")
	c.Assert(*params.ReturnURL, qt.Equals, "https://app.example.com/payment/{CHECKOUT_SESSION_ID}")
	c.Assert(params.SubscriptionData, qt.IsNil)
	c.Assert(params.Metadata[MetadataKeyProcessID], qt.Equals, "656e6f7567682062797465732100")
	c.Assert(params.PaymentIntentData.Metadata[MetadataKeyWalletTopUpOrg], qt.Equals,
		"0x000000000000000000000000000000000000bEEF")

	c.Assert(params.LineItems, qt.HasLen, 2)
	for i, want := range []struct {
		name  string
		cents pricing.Cents
	}{
		{"voting process (600 eligible voters)", 29_000},
		{"email 2FA", 600},
	} {
		item := params.LineItems[i]
		c.Assert(*item.Quantity, qt.Equals, int64(1))
		c.Assert(*item.PriceData.Currency, qt.Equals, "eur")
		c.Assert(*item.PriceData.TaxBehavior, qt.Equals, "exclusive")
		c.Assert(*item.PriceData.UnitAmount, qt.Equals, int64(want.cents))
		c.Assert(*item.PriceData.ProductData.Name, qt.Equals, want.name)
	}
}

// stubRefund points the Stripe client at a stub that answers GET /v1/refunds/{id} with status,
// or with a 500 when status is empty.
func stubRefund(t *testing.T, status stripeapi.RefundStatus) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": path.Base(r.URL.Path), "object": "refund", "status": status,
		})
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

// TestHandleRefundRereadsSucceeded: a succeeded refund event is checked against the refund as
// Stripe has it now. One that failed or was canceled since (its failed update handled first, and
// finding nothing to reverse) debits nothing, and a refund that cannot be read is retried.
func TestHandleRefundRereadsSucceeded(t *testing.T) {
	c := qt.New(t)
	event := &stripeapi.Event{
		ID:   "evt_refund",
		Type: stripeapi.EventTypeRefundUpdated,
		Data: &stripeapi.EventData{Raw: json.RawMessage(
			`{"id":"re_undone","object":"refund","status":"succeeded","amount":1000,"payment_intent":"pi_topup"}`)},
	}

	// no debit is attempted: the service has no database, so one would panic
	for _, status := range []stripeapi.RefundStatus{stripeapi.RefundStatusFailed, stripeapi.RefundStatusCanceled} {
		stubRefund(t, status)
		c.Assert((&Service{}).handleRefund(event), qt.IsNil)
	}
	stubRefund(t, "")
	c.Assert((&Service{}).handleRefund(event), qt.ErrorMatches, "failed to re-read refund re_undone.*")
}
