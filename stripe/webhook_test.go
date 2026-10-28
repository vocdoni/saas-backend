package stripe

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
)

// stubProductNotFound points the Stripe client at a stub that answers every request with
// resource_missing, so GET /v1/products/{id} fails.
func stubProductNotFound(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

// TestParseInvoiceFromEventNonSubscription pins that an invoice without a subscription parent
// (a one-time payment receipt, or a manual invoice) is ignored rather than failing the event.
func TestParseInvoiceFromEventNonSubscription(t *testing.T) {
	c := qt.New(t)

	for name, raw := range map[string]string{
		"parent omitted":              `{"id":"in_1","status":"paid","effective_at":1700000000}`,
		"parent without subscription": `{"id":"in_1","status":"paid","effective_at":1700000000,"parent":{}}`,
	} {
		c.Run(name, func(c *qt.C) {
			event := &stripeapi.Event{
				ID:   "evt_test",
				Type: stripeapi.EventTypeInvoicePaymentSucceeded,
				Data: &stripeapi.EventData{Raw: json.RawMessage(raw)},
			}
			info, err := parseInvoiceFromEvent(event)
			c.Assert(err, qt.IsNil)
			c.Assert(info, qt.IsNil)

			// the handler must return without touching the database, which is nil here
			c.Assert((&Service{}).HandleEvent(event), qt.IsNil)
		})
	}
}

// TestParseInvoiceFromEventPermanentErrors pins that a subscription invoice that can never be
// recorded is marked permanent, so the webhook drops it instead of Stripe retrying it for days.
func TestParseInvoiceFromEventPermanentErrors(t *testing.T) {
	c := qt.New(t)

	const parent = `"parent":{"subscription_details":{"metadata":{"address":"0x00000000000000000000000000000000000000aa"}}}`
	for name, raw := range map[string]string{
		"malformed payload":      `{"id":`,
		"missing effective date": `{"id":"in_1","status":"paid",` + parent + `}`,
		"missing address":        `{"id":"in_1","status":"paid","effective_at":1700000000,"parent":{"subscription_details":{}}}`,
		"not paid":               `{"id":"in_1","status":"open","effective_at":1700000000,` + parent + `}`,
	} {
		c.Run(name, func(c *qt.C) {
			_, err := parseInvoiceFromEvent(&stripeapi.Event{
				ID:   "evt_test",
				Type: stripeapi.EventTypeInvoicePaymentSucceeded,
				Data: &stripeapi.EventData{Raw: json.RawMessage(raw)},
			})
			c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue, qt.Commentf("%v", err))
		})
	}
}

// TestHandleProductUpsertFetchError pins that a failed re-fetch of the product returns an error
// naming the product instead of dereferencing the nil product it returned.
func TestHandleProductUpsertFetchError(t *testing.T) {
	c := qt.New(t)
	stubProductNotFound(t)

	service := &Service{client: NewClient(&Config{APIKey: "sk_test_stub", WebhookSecret: "whsec_stub"})}
	event := &stripeapi.Event{
		ID:   "evt_test",
		Type: stripeapi.EventTypeProductUpdated,
		Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"prod_1","object":"product","active":true,` +
			`"metadata":{"organization":"{}","votingTypes":"{}","features":"{}"}}`)},
	}

	err := service.HandleEvent(event)
	c.Assert(err, qt.IsNotNil)
	c.Assert(err.Error(), qt.Contains, "prod_1")
}
