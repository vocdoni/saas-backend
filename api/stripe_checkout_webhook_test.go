package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/util"
)

// installStripeWebhookService builds a real stripe.Service with the mock webhook secret and
// installs it as the API's webhook handler for one test. Everything downstream of the HTTP
// endpoint — signature validation, event parsing, the wallet credit — is the production code path.
func installStripeWebhookService(t *testing.T) {
	t.Helper()
	service := newStripeService(t)
	previous := testAPI.stripeHandlers
	testAPI.stripeHandlers = NewStripeHandlers(service)
	t.Cleanup(func() { testAPI.stripeHandlers = previous })
}

// postSignedStripeEvent signs an event envelope for the given session object exactly as
// Stripe would (v1 signature scheme over the raw payload) and POSTs it to the public
// webhook endpoint. It returns the response status code. eventID matters: Stripe retries
// reuse the id (deduplicated in memory), while a second replica sees a fresh id — pass a
// new one to exercise the durable idempotency guards.
func postSignedStripeEvent(t *testing.T, secret, eventID, eventType string, object map[string]any) int {
	t.Helper()
	c := qt.New(t)
	payload, err := json.Marshal(map[string]any{
		"id": eventID,
		// top-level "object":"event" marks this a snapshot event; v87's ConstructEvent
		// peeks at it and rejects anything else as a thin (v2) event notification
		"object": "event",
		// ConstructEvent rejects events whose api_version does not match the SDK's pin
		"api_version": stripeapi.APIVersion,
		"type":        eventType,
		"data":        map[string]any{"object": object},
	})
	c.Assert(err, qt.IsNil)
	signed := stripewebhook.GenerateTestSignedPayload(&stripewebhook.UnsignedPayload{
		Payload: payload,
		Secret:  secret,
	})
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://%s:%d/subscriptions/webhook", testHost, testPort),
		bytes.NewReader(payload))
	c.Assert(err, qt.IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", signed.Header)
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// checkoutSessionObject builds the wire shape of a checkout session as it arrives inside
// a webhook event.
func checkoutSessionObject(
	sessionID, paymentStatus string, amountSubtotal pricing.Cents, metadata map[string]string,
) map[string]any {
	return map[string]any{
		"id":              sessionID,
		"object":          "checkout.session",
		"mode":            "payment",
		"status":          "complete",
		"payment_status":  paymentStatus,
		"amount_subtotal": amountSubtotal,
		"amount_total":    amountSubtotal, // tax not exercised here
		"metadata":        metadata,
	}
}

const testWebhookSecret = "mockWebhookSecret" // must match newStripeService

// assertWallet asserts an organization's balance and how many history rows of kind it holds.
func assertWallet(t *testing.T, org common.Address, balance pricing.Cents, kind string, rows int) {
	t.Helper()
	c := qt.New(t)
	wallet, err := testDB.Wallet(org)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, balance)
	_, entries, err := testDB.WalletLedger(org, 1, 100)
	c.Assert(err, qt.IsNil)
	n := 0
	for _, e := range entries {
		if e.Kind == kind {
			n++
		}
	}
	c.Assert(n, qt.Equals, rows, qt.Commentf("%s rows", kind))
}

// TestStripeCheckoutWebhookWalletTopUp: a verified free-form top-up credits the wallet with the
// net (pre-tax) amount through the signed endpoint, exactly once across replays, and records the
// payment intent a later refund names.
func TestStripeCheckoutWebhookWalletTopUp(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	token := testCreateUser(t, "webhooktopup1234")
	orgAddress := testCreateOrganization(t, token)

	sessionID := "cs_topup_" + util.RandomHex(8)
	topUpObject := checkoutSessionObject(sessionID, "paid", 50_000, map[string]string{
		stripe.MetadataKeyWalletTopUpOrg: orgAddress.String(),
	})
	// amount_total carries VAT on top of the net amount; only the net is credited
	topUpObject["amount_total"] = int64(60_500)
	topUpObject["payment_intent"] = "pi_" + sessionID

	for range 2 { // the second is a fresh-id replay (another replica): no double credit
		code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
			"checkout.session.completed", topUpObject)
		c.Assert(code, qt.Equals, http.StatusOK)
	}
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(50_000))
	c.Assert(wallet.History, qt.HasLen, 1)
	c.Assert(wallet.History[0].Kind, qt.Equals, db.WalletEntryTopUp)
	c.Assert(wallet.History[0].AmountCents, qt.Equals, pricing.Cents(50_000))
	topUp, err := testDB.WalletTopUpByPaymentIntent("pi_" + sessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(topUp.GrossCents, qt.Equals, pricing.Cents(60_500))
}

// TestStripeCheckoutWebhookPermanentlyInvalidEvents: metadata Stripe already delivered is
// immutable, so an event carrying garbage there can never become valid — answering 500
// would make Stripe retry it for about three days and bury real incidents in the
// endpoint's failure queue. The endpoint accepts and drops them instead, changing nothing.
func TestStripeCheckoutWebhookPermanentlyInvalidEvents(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "badmetadata12345")
	orgAddress := testCreateOrganization(t, token)

	// a top-up whose organization is not an address: nothing to credit, ever
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject("cs_badorg_"+util.RandomHex(4), "paid", 50_000, map[string]string{
			stripe.MetadataKeyWalletTopUpOrg: "not-an-address",
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 0)
}

// TestStripeCheckoutWebhookDelayedWalletTopUp: a free-form top-up paid by a delayed method (SEPA)
// completes unpaid, which credits nothing, and is credited once when its payment succeeds.
func TestStripeCheckoutWebhookDelayedWalletTopUp(t *testing.T) {
	installStripeWebhookService(t)
	token := testCreateUser(t, "webhookasync1234")
	orgAddress := testCreateOrganization(t, token)

	sessionID := "cs_async_" + util.RandomHex(8)
	metadata := map[string]string{stripe.MetadataKeyWalletTopUpOrg: orgAddress.String()}
	unpaid := checkoutSessionObject(sessionID, "unpaid", 20_000, metadata)
	paid := checkoutSessionObject(sessionID, "paid", 20_000, metadata)
	paid["payment_intent"] = "pi_" + sessionID

	post := func(eventType string, object map[string]any) {
		qt.Assert(t, postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), eventType, object),
			qt.Equals, http.StatusOK)
	}
	post("checkout.session.completed", unpaid)
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 0)
	post("checkout.session.async_payment_succeeded", paid)
	post("checkout.session.async_payment_succeeded", paid) // a fresh-id replay
	assertWallet(t, orgAddress, 20_000, db.WalletEntryTopUp, 1)
}
