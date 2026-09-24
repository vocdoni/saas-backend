package stripe

import (
	"os"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestLiveCheckoutSessionLifecycle exercises the one-time checkout wire shape against
// Stripe's real test-mode API — the combination of price_data, automatic_tax,
// tax_id_collection and invoice_creation is validated server-side by Stripe in ways no
// fake can, and a rejected combination would otherwise only surface in production.
//
// Opt-in: it runs only when STRIPE_TEST_SECRET_KEY holds a test-mode key (sk_test_…) and
// needs network access. Paying the session is deliberately out of scope — a ui_mode
// "custom" session can only be confirmed from the embedded Payment Element in a browser;
// this covers create → read → expire.
func TestLiveCheckoutSessionLifecycle(t *testing.T) {
	key := os.Getenv("STRIPE_TEST_SECRET_KEY")
	if key == "" {
		t.Skip("STRIPE_TEST_SECRET_KEY not set; skipping live Stripe test-mode check")
	}
	c := qt.New(t)
	c.Assert(strings.HasPrefix(key, "sk_test_"), qt.IsTrue,
		qt.Commentf("refusing to run against a non-test-mode key"))

	client := NewClient(&Config{APIKey: key, WebhookSecret: "whsec_unused"})

	created, err := client.CreatePaymentCheckoutSession(&PaymentSessionParams{
		LineItems: []PaymentLineItem{
			{Description: "voting process (600 eligible voters)", AmountCents: 29_000},
			{Description: "email 2FA", AmountCents: 600},
		},
		Metadata: map[string]string{
			MetadataKeyProcessID:   "656e6f7567682062797465732100",
			MetadataKeyRequestedBy: "livetest@example.com",
		},
		CustomerEmail: "livetest@example.com",
		ReturnURL:     "https://example.com/payment",
	})
	c.Assert(err, qt.IsNil)
	session := paymentSessionInfo(created)
	c.Assert(session.ClientSecret, qt.Not(qt.Equals), "")
	c.Assert(session.Status, qt.Equals, SessionStatusOpen)
	c.Assert(session.PaymentStatus, qt.Equals, PaymentStatusUnpaid)

	// GetPaymentSession and ExpirePaymentSession have no service state; a zero Service
	// is the documented way to reach them without a database
	var service Service
	fetched, err := service.GetPaymentSession(session.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(fetched.Status, qt.Equals, SessionStatusOpen)

	c.Assert(client.ExpireCheckoutSession(session.ID), qt.IsNil)
	expired, err := service.GetPaymentSession(session.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(expired.Status, qt.Equals, SessionStatusExpired)
}
