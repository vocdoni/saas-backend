package stripe

import (
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
)

// TestHandleEventIgnoresRetiredEvents: the subscription, invoice and product events of the retired
// subscription model are acknowledged and ignored, so Stripe stops redelivering them once the
// subscriptions are cancelled.
func TestHandleEventIgnoresRetiredEvents(t *testing.T) {
	c := qt.New(t)
	s := &Service{}
	for _, eventType := range []stripeapi.EventType{
		stripeapi.EventTypeCustomerSubscriptionCreated,
		stripeapi.EventTypeCustomerSubscriptionUpdated,
		stripeapi.EventTypeCustomerSubscriptionDeleted,
		stripeapi.EventTypeInvoicePaymentSucceeded,
		stripeapi.EventTypeProductCreated,
		stripeapi.EventTypeProductUpdated,
		stripeapi.EventTypeProductDeleted,
	} {
		c.Assert(s.HandleEvent(&stripeapi.Event{ID: "evt_retired", Type: eventType}), qt.IsNil, qt.Commentf("%s", eventType))
	}
}
