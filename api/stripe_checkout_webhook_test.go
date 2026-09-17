package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// installStripeWebhookService builds a real stripe.Service with the mock webhook secret,
// wires webhook-triggered publication, and installs it as the API's webhook handler for
// one test. Everything downstream of the HTTP endpoint — signature validation, event
// parsing, fulfillment, publication — is the production code path.
func installStripeWebhookService(t *testing.T) {
	t.Helper()
	service := newStripeService(t)
	service.OnProcessPaid = testAPI.publishPaidProcess
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
		// top-level "object":"event" marks this a snapshot event; v86's ConstructEvent
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
func checkoutSessionObject(sessionID, paymentStatus string, amountSubtotal int64, metadata map[string]string) map[string]any {
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

// TestStripeCheckoutWebhookProcessPayment drives the full process-payment flow through
// the signed webhook endpoint: a completed-but-unpaid session (delayed payment method)
// parks the payment as processing, the async success marks it paid and publishes the
// process server-side, and replays — same event id or fresh one — change nothing.
func TestStripeCheckoutWebhookProcessPayment(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "webhookpass12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	// open a checkout (fake gateway) so a pending payment with a session id exists,
	// carrying the requesting user for the server-side publication
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")
	sessionID := checkout.SessionID
	processMeta := map[string]string{
		stripe.MetadataKeyProcessID:   pid,
		stripe.MetadataKeyRequestedBy: "ignored-here", // fulfillment reads RequestedBy from the stored payment
	}

	// a tampered payload (wrong signing secret) is rejected and changes nothing
	code := postSignedStripeEvent(t, "whsec_wrong_secret", "evt_"+util.RandomHex(8),
		"checkout.session.completed", checkoutSessionObject(sessionID, "paid", 1_515, processMeta))
	c.Assert(code, qt.Equals, http.StatusInternalServerError)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)

	// delayed payment method: session completes while still unpaid -> processing, no publish
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", checkoutSessionObject(sessionID, "unpaid", 1_515, processMeta))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentProcessing)

	// the async payment clears: paid, and the process publishes with no client involved
	paidEventID := "evt_" + util.RandomHex(8)
	paidObject := checkoutSessionObject(sessionID, "paid", 1_515, processMeta)
	code = postSignedStripeEvent(t, testWebhookSecret, paidEventID,
		"checkout.session.async_payment_succeeded", paidObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	pollProcessPublished(t, token, pid)

	// replay with the same event id (a Stripe retry) and with a fresh id (a second
	// replica, which has no in-memory dedup): both are accepted and neither publishes
	// twice nor moves the payment
	code = postSignedStripeEvent(t, testWebhookSecret, paidEventID,
		"checkout.session.async_payment_succeeded", paidObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.async_payment_succeeded", paidObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)

	// a completed session carrying none of our metadata (the legacy subscription
	// checkout) passes through untouched
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", checkoutSessionObject("cs_sub_flow", "paid", 19_900, nil))
	c.Assert(code, qt.Equals, http.StatusOK)
}

// TestStripeCheckoutWebhookCardSuccess: the common card flow — a single
// checkout.session.completed already carrying payment_status "paid" — marks the payment
// paid and publishes in one step, through the signed endpoint.
func TestStripeCheckoutWebhookCardSuccess(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "webhookcard12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject(checkout.SessionID, "paid", 1_515, map[string]string{
			stripe.MetadataKeyProcessID: pid,
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	pollProcessPublished(t, token, pid)
}

// TestStripeCheckoutWebhookSessionMismatch: money arriving for a session the process no
// longer references (an orphaned session that slipped past expiry) is accepted at the
// HTTP level — Stripe must not retry forever — but logged for manual reconciliation and
// the payment state is left untouched.
func TestStripeCheckoutWebhookSessionMismatch(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "webhookmismatch1")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	_ = requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject("cs_orphaned_session", "paid", 1_515, map[string]string{
			stripe.MetadataKeyProcessID: pid,
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
}

// TestStripeCheckoutWebhookPaymentFailed: the async failure returns the payment to the
// payable state through the signed endpoint.
func TestStripeCheckoutWebhookPaymentFailed(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "webhookfail12345")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.async_payment_failed",
		checkoutSessionObject(checkout.SessionID, "unpaid", 1_515, map[string]string{
			stripe.MetadataKeyProcessID: pid,
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)

	// failed is payable again: a new checkout opens a replacement session
	again := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")
	c.Assert(again.SessionID, qt.Not(qt.Equals), checkout.SessionID)
}

// TestStripeCheckoutWebhookWalletTopUp: a verified top-up credits the integrator wallet
// with the net (pre-tax) amount through the signed endpoint, exactly once across replays.
func TestStripeCheckoutWebhookWalletTopUp(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	token := testCreateUser(t, "webhooktopup1234")
	integratorAddr := testCreateOrganization(t, token)
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	sessionID := "cs_topup_" + util.RandomHex(8)
	topUpObject := checkoutSessionObject(sessionID, "paid", 50_000, map[string]string{
		stripe.MetadataKeyWalletTopUpOrg: integratorAddr.String(),
	})
	// amount_total carries VAT on top of the net amount; only the net is credited
	topUpObject["amount_total"] = int64(60_500)

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", topUpObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(50_000))

	// a fresh-id replay (second replica) cannot double-credit; the ledger has one row
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", topUpObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(50_000))
	c.Assert(wallet.Ledger, qt.HasLen, 1)
	c.Assert(wallet.Ledger[0].AmountCents, qt.Equals, int64(50_000))
}
