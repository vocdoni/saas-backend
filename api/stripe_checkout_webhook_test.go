package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v86"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
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

// TestStripeCheckoutWebhookStampsBranding: paying a process that carries the branding
// add-on stamps the organization branding-paid, so the org's next process is no longer
// charged the once-per-organization €149 branding fee.
func TestStripeCheckoutWebhookStampsBranding(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "brandingpaid1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)

	// process A selects branding: 2 voters -> free base, +€149 branding = 14900 cents
	reqA := newVotingProcessRequest(orgAddress, memberIDs(members))
	reqA.AddOns = db.ProcessAddOns{Branding: true}
	createdA := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, reqA, processesCreateEndpoint)
	priceA := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", createdA.ProcessID, "price")
	c.Assert(priceA.TotalCents, qt.Equals, int64(14_900))

	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", createdA.ProcessID, "checkout")

	// pay it: the fulfillment stamps the organization branding-paid
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject(checkout.SessionID, "paid", 14_900, map[string]string{
			stripe.MetadataKeyProcessID: createdA.ProcessID,
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)

	// process B also selects branding, but the org already paid it: no branding line, free
	reqB := newVotingProcessRequest(orgAddress, memberIDs(members))
	reqB.AddOns = db.ProcessAddOns{Branding: true}
	createdB := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, reqB, processesCreateEndpoint)
	priceB := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", createdB.ProcessID, "price")
	c.Assert(priceB.TotalCents, qt.Equals, int64(0))
	for _, line := range priceB.Lines {
		c.Assert(line.Kind, qt.Not(qt.Equals), pricing.LineBranding)
	}
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

// TestStripeCheckoutWebhookReplayPublishesStrandedProcess: fulfillment is two steps —
// win the paid CAS, then publish. A crash in between leaves the money taken and the
// process unpublished, and every later replay loses the CAS. Losing it must therefore
// still run the side effects, or that process never publishes at all.
func TestStripeCheckoutWebhookReplayPublishesStrandedProcess(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)

	token := testCreateUser(t, "replaypass123456")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	// the crash: the payment is recorded paid (CAS won) but publication never ran
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)

	// Stripe retries the event: the CAS is already lost, yet the replay publishes
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject(checkout.SessionID, "paid", 1_515, map[string]string{
			stripe.MetadataKeyProcessID: pid,
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
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
	integratorAddr := testCreateOrganization(t, token)
	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 1}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	// a top-up whose organization is not an address: nothing to credit, ever
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed",
		checkoutSessionObject("cs_badorg_"+util.RandomHex(4), "paid", 50_000, map[string]string{
			stripe.MetadataKeyWalletTopUpOrg: "not-an-address",
		}))
	c.Assert(code, qt.Equals, http.StatusOK)
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))
	c.Assert(wallet.Ledger, qt.HasLen, 0)

	// a process payment whose process id is not an ObjectID, on both the completed and
	// the async-failed event: no process to reconcile it against, ever
	for _, eventType := range []string{"checkout.session.completed", "checkout.session.async_payment_failed"} {
		code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), eventType,
			checkoutSessionObject("cs_badpid_"+util.RandomHex(4), "paid", 1_515, map[string]string{
				stripe.MetadataKeyProcessID: "not-an-object-id",
			}))
		c.Assert(code, qt.Equals, http.StatusOK)
	}
}

// TestStripeRefundFailedWebhook covers the refund that does not stick: Stripe accepted it,
// the draft was deleted on the strength of that, and the refund then failed. The process
// cannot come back, so the only correct reaction is to stop the payment looking settled and
// say loudly that a human must reconcile it.
func TestStripeRefundFailedWebhook(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        common.HexToAddress("0x000000000000000000000000000000000000beef"),
		CheckoutSessionID: "cs_refund_failed",
		AmountCents:       1_500,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(processID, "cs_refund_failed", db.ProcessCharge{PaymentIntentID: "pi_refund_failed"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunded, err := testDB.MarkProcessPaymentRefunded(processID, "re_failed", 1_500)
	c.Assert(err, qt.IsNil)
	c.Assert(refunded, qt.IsTrue)

	refundObject := func(status, rawProcessID string) map[string]any {
		return map[string]any{
			"id":       "re_failed",
			"object":   "refund",
			"amount":   1_500,
			"status":   status,
			"metadata": map[string]string{"voting_process_id": rawProcessID},
		}
	}

	// a refund still on its way changes nothing
	status := postSignedStripeEvent(t, testWebhookSecret, "evt_refund_pending",
		"charge.refund.updated", refundObject("pending", processID.Hex()))
	c.Assert(status, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)

	// one that failed puts the payment back to paid, keeping the refund id to reconcile
	status = postSignedStripeEvent(t, testWebhookSecret, "evt_refund_failed",
		"charge.refund.updated", refundObject("failed", processID.Hex()))
	c.Assert(status, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.RefundID, qt.Equals, "re_failed")

	// a refund naming no process is permanently invalid: answered 200 so Stripe stops
	status = postSignedStripeEvent(t, testWebhookSecret, "evt_refund_failed_bad",
		"charge.refund.updated", refundObject("failed", "not-an-object-id"))
	c.Assert(status, qt.Equals, http.StatusOK)
}

// TestStripeLateCensusTopUpRefunded: a census top-up paid after its draft was deleted and
// refunded bought nothing, so the webhook refunds it on the spot — under the per-intent key
// every refund of the process uses — instead of raising an envelope nobody can spend.
func TestStripeLateCensusTopUpRefunded(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)

	// Stripe's API, stubbed: the one call this path makes is the refund
	var refundBodies, refundKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Check(r.URL.Path, qt.Equals, "/v1/refunds")
		c.Check(r.ParseForm(), qt.IsNil)
		refundBodies = append(refundBodies, r.PostForm.Get("payment_intent"))
		refundKeys = append(refundKeys, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"re_late","object":"refund","status":"succeeded"}`))
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend,
		stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(srv.URL)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })

	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        common.HexToAddress("0x000000000000000000000000000000000000beef"),
		CheckoutSessionID: "cs_late_first",
		AmountCents:       1_500,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(processID, "cs_late_first", db.ProcessCharge{PaymentIntentID: "pi_late_first"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunded, err := testDB.MarkProcessPaymentRefunded(processID, "re_first", 1_500)
	c.Assert(err, qt.IsNil)
	c.Assert(refunded, qt.IsTrue)

	session := checkoutSessionObject("cs_late_topup", "paid", 500, map[string]string{
		stripe.MetadataKeyProcessID:         processID.Hex(),
		stripe.MetadataKeyProcessTopUpCents: "2000",
	})
	session["payment_intent"] = "pi_late_topup"
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_late_topup", "checkout.session.completed", session)
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(refundBodies, qt.DeepEquals, []string{"pi_late_topup"})
	c.Assert(refundKeys, qt.DeepEquals, []string{"refund:" + processID.Hex() + ":pi_late_topup"})

	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.AmountCents, qt.Equals, int64(1_500))
}
