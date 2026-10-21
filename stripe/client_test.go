package stripe

import (
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"
)

func TestCheckoutSessionStatus(t *testing.T) {
	c := qt.New(t)

	// A subscription-mode session with every sub-object present.
	full := checkoutSessionStatus(&stripeapi.CheckoutSession{
		Status:          stripeapi.CheckoutSessionStatusComplete,
		CustomerDetails: &stripeapi.CheckoutSessionCustomerDetails{Email: "creator@example.com"},
		Subscription:    &stripeapi.Subscription{Status: stripeapi.SubscriptionStatusActive},
	})
	c.Assert(full, qt.DeepEquals, &CheckoutSessionStatus{
		Status:             "complete",
		CustomerEmail:      "creator@example.com",
		SubscriptionStatus: "active",
	})

	// A one-time (mode "payment") session has no subscription, and an open session
	// has no customer details yet — neither must panic.
	oneTime := checkoutSessionStatus(&stripeapi.CheckoutSession{
		Status: stripeapi.CheckoutSessionStatusOpen,
	})
	c.Assert(oneTime, qt.DeepEquals, &CheckoutSessionStatus{Status: "open"})
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
