package stripe

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"
)

func TestCheckoutSessionStatus(t *testing.T) {
	c := qt.New(t)

	// A session with customer details and the organization address stamped at creation.
	full := checkoutSessionStatus(&stripeapi.CheckoutSession{
		Status:          stripeapi.CheckoutSessionStatusComplete,
		CustomerDetails: &stripeapi.CheckoutSessionCustomerDetails{Email: "creator@example.com"},
		Metadata:        map[string]string{MetadataKeyWalletTopUpOrg: "0x00000000000000000000000000000000000000AA"},
	})
	c.Assert(full, qt.DeepEquals, &CheckoutSessionStatus{
		Status:        "complete",
		CustomerEmail: "creator@example.com",
		OrgAddress:    "0x00000000000000000000000000000000000000AA",
	})

	// An open session has no customer details yet and no organization (an orphan).
	oneTime := checkoutSessionStatus(&stripeapi.CheckoutSession{
		Status: stripeapi.CheckoutSessionStatusOpen,
	})
	c.Assert(oneTime, qt.DeepEquals, &CheckoutSessionStatus{Status: "open"})
	c.Assert(oneTime.OrgAddress, qt.Equals, "")

	// A wallet top-up resolves to the organization whose wallet it credits.
	topUp := checkoutSessionStatus(&stripeapi.CheckoutSession{
		Status:   stripeapi.CheckoutSessionStatusComplete,
		Metadata: map[string]string{MetadataKeyWalletTopUpOrg: "0x000000000000000000000000000000000000bEEF"},
	})
	c.Assert(topUp.OrgAddress, qt.Equals, "0x000000000000000000000000000000000000bEEF")
}

// TestValidateWebhookEventAPIVersion pins the deploy precondition of the stripe-go bump: an event
// is accepted only on the SDK's release train (endive), so the webhook endpoint must be moved to
// it before this ships — a basil endpoint's events are refused, and Stripe keeps retrying them.
func TestValidateWebhookEventAPIVersion(t *testing.T) {
	c := qt.New(t)
	const secret = "whsec_test"
	client := NewClient(&Config{WebhookSecret: secret})
	signed := func(apiVersion string) ([]byte, string) {
		payload := fmt.Appendf(nil, `{"id":"evt_v","object":"event","type":"ping","api_version":%q}`, apiVersion)
		p := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{Payload: payload, Secret: secret})
		return p.Payload, p.Header
	}

	event, err := client.ValidateWebhookEvent(signed(stripeapi.APIVersion))
	c.Assert(err, qt.IsNil)
	c.Assert(event.ID, qt.Equals, "evt_v")

	_, err = client.ValidateWebhookEvent(signed("2025-08-27.basil"))
	c.Assert(err, qt.ErrorMatches, ".*API version.*")
}

// TestGetCustomerByAddress tells the three answers of a customer search apart: a hit is the
// customer, an empty result wraps ErrCustomerNotFound, and a failed search is a Stripe error
// a caller must not read as "no customer".
func TestGetCustomerByAddress(t *testing.T) {
	c := qt.New(t)
	status, data := http.StatusOK, `[]`
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, qt.Equals, "/v1/customers/search")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"object":"search_result","url":"/v1/customers/search","has_more":false,"data":` + data + `}`))
	}))
	client := NewClient(&Config{APIKey: "sk_test_stub"})

	_, err := client.GetCustomerByAddress("0x01")
	c.Assert(err, qt.ErrorIs, ErrCustomerNotFound)

	data = `[{"id":"cus_1","object":"customer"}]`
	customer, err := client.GetCustomerByAddress("0x01")
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_1")

	status, data = http.StatusInternalServerError, `[]`
	_, err = client.GetCustomerByAddress("0x01")
	c.Assert(err, qt.ErrorMatches, ".*failed to search customers.*")
	c.Assert(err, qt.Not(qt.ErrorIs), ErrCustomerNotFound)
}

// TestCheckoutSessionsOfCustomer lists a customer's checkout sessions through the customer
// filter: none is an empty result, not an error, and a failed list is a Stripe error.
func TestCheckoutSessionsOfCustomer(t *testing.T) {
	c := qt.New(t)
	status, data := http.StatusOK, `[]`
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Assert(r.URL.Path, qt.Equals, "/v1/checkout/sessions")
		c.Assert(r.URL.Query().Get("customer"), qt.Equals, "cus_1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"object":"list","url":"/v1/checkout/sessions","has_more":false,"data":` + data + `}`))
	}))
	client := NewClient(&Config{APIKey: "sk_test_stub"})

	sessions, err := client.CheckoutSessionsOfCustomer("cus_1")
	c.Assert(err, qt.IsNil)
	c.Assert(sessions, qt.HasLen, 0)

	data = `[{"id":"cs_1","object":"checkout.session","metadata":{"` + MetadataKeyWalletTopUpOrg + `":"0x01"}},` +
		`{"id":"cs_2","object":"checkout.session"}]`
	sessions, err = client.CheckoutSessionsOfCustomer("cus_1")
	c.Assert(err, qt.IsNil)
	c.Assert(sessions, qt.HasLen, 2)
	c.Assert(sessions[0].Metadata[MetadataKeyWalletTopUpOrg], qt.Equals, "0x01")

	status, data = http.StatusInternalServerError, `[]`
	_, err = client.CheckoutSessionsOfCustomer("cus_1")
	c.Assert(err, qt.ErrorMatches, ".*failed to list the checkout sessions of customer cus_1.*")
}
