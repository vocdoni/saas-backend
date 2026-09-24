package stripe

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestBuildPaymentSessionParams pins the wire shape of a one-time checkout session: mode
// payment, embedded UI, tax exclusive EUR price_data, invoice creation, and the routing
// metadata on the session itself (a payment session has no SubscriptionData).
func TestBuildPaymentSessionParams(t *testing.T) {
	c := qt.New(t)

	params := buildPaymentSessionParams(&PaymentSessionParams{
		LineItems: []PaymentLineItem{
			{Description: "voting process (600 eligible voters)", AmountCents: 29_000},
			{Description: "email 2FA", AmountCents: 600},
		},
		Metadata: map[string]string{
			MetadataKeyProcessID:   "656e6f7567682062797465732100",
			MetadataKeyRequestedBy: "admin@example.com",
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

	c.Assert(params.LineItems, qt.HasLen, 2)
	for i, want := range []struct {
		name  string
		cents int64
	}{
		{"voting process (600 eligible voters)", 29_000},
		{"email 2FA", 600},
	} {
		item := params.LineItems[i]
		c.Assert(*item.Quantity, qt.Equals, int64(1))
		c.Assert(*item.PriceData.Currency, qt.Equals, "eur")
		c.Assert(*item.PriceData.TaxBehavior, qt.Equals, "exclusive")
		c.Assert(*item.PriceData.UnitAmount, qt.Equals, want.cents)
		c.Assert(*item.PriceData.ProductData.Name, qt.Equals, want.name)
	}
}
