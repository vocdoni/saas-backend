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
