package stripe

import (
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
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
