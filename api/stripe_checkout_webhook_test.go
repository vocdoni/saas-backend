package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/util"
)

// installStripeWebhookService builds a real stripe.Service with the mock webhook secret,
// wires the process follow-ups of a top-up, and installs it as the API's webhook handler for
// one test. Everything downstream of the HTTP endpoint — signature validation, event
// parsing, the wallet credit, the debit and publication — is the production code path.
func installStripeWebhookService(t *testing.T) {
	t.Helper()
	service := newStripeService(t)
	service.OnProcessTopUp = testAPI.applyProcessTopUp
	service.OnProcessCheckoutDropped = testAPI.dropProcessCheckout
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

// openProcessTopUp opens the top-up checkout of a standard organization's draft (fake gateway)
// and returns the session object its paid webhook carries: the metadata the checkout set, and
// the amount it charges, net.
func openProcessTopUp(t *testing.T, fake *fakePaymentGW, token, pid string) (string, map[string]any) {
	t.Helper()
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")
	metadata := fake.created[len(fake.created)-1].Metadata
	return checkout.SessionID, checkoutSessionObject(checkout.SessionID, "paid", checkout.AmountCents, metadata)
}

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

// TestTopUpWebhookPublishesProcess drives a process top-up through the signed endpoint: a
// tampered event changes nothing, a completed-but-unpaid session (delayed method) credits
// nothing, the async success credits the wallet, consumes the pending payment, debits the price
// and publishes with no client involved; replays — same event id or a fresh one — credit once and
// debit once, and an expiry after completion is a no-op.
func TestTopUpWebhookPublishesProcess(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "webhookpass12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	const balance = 400
	fundWallet(t, orgAddress, balance)
	sessionID, paidObject := openProcessTopUp(t, fake, token, pid)
	c.Assert(paidObject["amount_subtotal"], qt.Equals, pricedProcessCents-balance)

	// a tampered payload (wrong signing secret) is rejected and changes nothing
	code := postSignedStripeEvent(t, "whsec_wrong_secret", "evt_"+util.RandomHex(8),
		"checkout.session.completed", paidObject)
	c.Assert(code, qt.Equals, http.StatusInternalServerError)
	assertWallet(t, orgAddress, balance, db.WalletEntryTopUp, 1)

	// delayed payment method: the session completes unpaid, nothing is credited or consumed
	unpaid := checkoutSessionObject(sessionID, "unpaid", pricedProcessCents-balance,
		fake.created[len(fake.created)-1].Metadata)
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", unpaid)
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsFalse)
	assertWallet(t, orgAddress, balance, db.WalletEntryTopUp, 1)

	// it clears: credited, debited, published as the requesting user
	paidEventID := "evt_" + util.RandomHex(8)
	code = postSignedStripeEvent(t, testWebhookSecret, paidEventID, "checkout.session.async_payment_succeeded", paidObject)
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(payment.CheckoutSessionID, qt.Equals, sessionID)
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 2)
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 1)

	// replays with the same event id (a Stripe retry) and a fresh one (another replica, no
	// in-memory dedup), and an expiry arriving late: one credit, one debit, nothing moves
	for _, event := range []struct{ id, kind string }{
		{paidEventID, "checkout.session.async_payment_succeeded"},
		{"evt_" + util.RandomHex(8), "checkout.session.completed"},
		{"evt_" + util.RandomHex(8), "checkout.session.expired"},
	} {
		code = postSignedStripeEvent(t, testWebhookSecret, event.id, event.kind, paidObject)
		c.Assert(code, qt.Equals, http.StatusOK)
	}
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 2)
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 1)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)

	// a completed session carrying none of our metadata (the subscription checkout) passes
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", checkoutSessionObject("cs_sub_flow", "paid", 19_900, nil))
	c.Assert(code, qt.Equals, http.StatusOK)
}

// TestStripeCheckoutWebhookStampsBranding: a top-up that pays a process carrying the branding
// add-on stamps the organization branding-paid, so the org's next process is no longer charged
// the once-per-organization €149 branding fee.
func TestStripeCheckoutWebhookStampsBranding(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "brandingpaid1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)

	// process A selects branding: 2 voters -> free base, +€149 branding = 14900 cents
	reqA := newVotingProcessRequest(orgAddress, memberIDs(members))
	reqA.AddOns = db.ProcessAddOns{Branding: true}
	createdA := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, reqA, processesCreateEndpoint)
	_, paid := openProcessTopUp(t, fake, token, createdA.ProcessID)
	c.Assert(paid["amount_subtotal"], qt.Equals, pricing.BrandingCents)
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, createdA.ProcessID)
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
	c.Assert(priceB.TotalCents, qt.Equals, pricing.Cents(0))
	for _, line := range priceB.Lines {
		c.Assert(line.Kind, qt.Not(qt.Equals), pricing.LineBranding)
	}
}

// TestTopUpWebhookStaleSessionKeepsCredit: money arriving for a session the process no longer
// waits on (replaced by a newer checkout, or orphaned) buys nothing for the process: it is
// credited to the wallet and stays there, and the pending payment is left alone.
func TestTopUpWebhookStaleSessionKeepsCredit(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "webhookmismatch1")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	_, paid := openProcessTopUp(t, fake, token, pid)
	paid["id"] = "cs_orphaned_session"

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryTopUp, 1)
	payment, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsFalse)
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
}

// TestTopUpWebhookRefusalKeepsCredit: a top-up that cannot pay its publish — here discounted below
// the shortfall, so the wallet still falls short — is acknowledged with a 200 (Stripe cannot fix
// it): the net amount stays in the wallet, the pending payment is consumed, and the user publishes
// by hand once the wallet covers the price.
func TestTopUpWebhookRefusalKeepsCredit(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "webhookunderpaid1")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	_, paid := openProcessTopUp(t, fake, token, pid)
	paid["total_details"] = map[string]any{"amount_discount": 500}
	paid["amount_total"] = int(pricedProcessCents) - 500

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents-500, db.WalletEntryTopUp, 1)
	payment, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsTrue)

	// a late expiry of the session already credited drops nothing
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.expired", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsTrue)

	// consumed: no longer an open checkout, so the manual publish is the 402 of the wallet
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPost, token, nil, "processes", pid, "publish")
	fundWallet(t, orgAddress, 500)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 1)
}

// TestTopUpWebhookFailedDropsCheckout: an async payment failure (or an expiry) credits nothing
// and drops the pending payment, so a new checkout opens a replacement session.
func TestTopUpWebhookFailedDropsCheckout(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "webhookfail12345")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	sessionID, paid := openProcessTopUp(t, fake, token, pid)
	paid["payment_status"] = "unpaid"

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.async_payment_failed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	_, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 0)

	again, _ := openProcessTopUp(t, fake, token, pid)
	c.Assert(again, qt.Not(qt.Equals), sessionID)
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

// TestTopUpWebhookHealsCrashBeforeConsume: the credit landed but the delivery died before
// consuming the pending payment. Stripe's redelivery credits nothing new, consumes it and
// publishes, debiting once.
func TestTopUpWebhookHealsCrashBeforeConsume(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "replaypass123456")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	sessionID, paid := openProcessTopUp(t, fake, token, pid)

	// the crash: the credit is in the wallet, the payment still pending and unconsumed
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: orgAddress, AmountCents: pricedProcessCents, IdempotencyKey: sessionID,
	}), qt.IsNil)

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
	assertWallet(t, orgAddress, 0, db.WalletEntryTopUp, 1)
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 1)
}

// TestStripeCheckoutWebhookRetriesTransientPublishFailure: a top-up whose auto-publish fails for a
// reason that clears on its own — here a concurrent publish holding the claim — must not be
// acknowledged: Stripe's redelivery is the only thing that ever comes back to publish it. The
// redelivery finds the payment consumed by this session and publishes, debiting once.
func TestStripeCheckoutWebhookRetriesTransientPublishFailure(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "retrypass1234567")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	_, paid := openProcessTopUp(t, fake, token, pid)

	// another publish holds the claim when the payment lands
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	eventID := "evt_" + util.RandomHex(8)
	code := postSignedStripeEvent(t, testWebhookSecret, eventID, "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusInternalServerError)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsTrue)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 0)

	// that publish gave up; Stripe redelivers the same event, and this time it publishes
	c.Assert(testDB.ClearVotingProcessPublishing(oid, owner), qt.IsNil)
	code = postSignedStripeEvent(t, testWebhookSecret, eventID, "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 1)
}

// TestTopUpWebhookReplayAfterCreditBackDoesNotDebit: a top-up paid a draft that was then deleted
// and credited back. Stripe can resend the event at any time: it must neither credit nor debit
// again, nor resurrect anything.
func TestTopUpWebhookReplayAfterCreditBackDoesNotDebit(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)

	token := testCreateUser(t, "replaycredit1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	_, paid := openProcessTopUp(t, fake, token, pid)

	// the first delivery credits and consumes, but the publish cannot start (claim held)
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusInternalServerError)
	c.Assert(testDB.ClearVotingProcessPublishing(oid, owner), qt.IsNil)
	// the draft is paid from the wallet (the debit a manual publish makes), then deleted
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	_, err = testAPI.chargeProcessWallet(processCharge{vp: vp, org: org, quote: pricing.Quote{TotalCents: pricedProcessCents}})
	c.Assert(err, qt.IsNil)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricedProcessCents) // the top-up, credited back

	code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryTopUp, 1)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 1)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryCreditBack, 1)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
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

	// a process top-up whose process id is not an ObjectID, on both the completed and the
	// async-failed event: no process to reconcile it against, ever — but the completed one
	// still paid for its credit
	for _, eventType := range []string{"checkout.session.completed", "checkout.session.async_payment_failed"} {
		code = postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), eventType,
			checkoutSessionObject("cs_badpid_"+util.RandomHex(4), "paid", pricedProcessCents, map[string]string{
				stripe.MetadataKeyWalletTopUpOrg: orgAddress.String(),
				stripe.MetadataKeyProcessID:      "not-an-object-id",
			}))
		c.Assert(code, qt.Equals, http.StatusOK)
	}
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryTopUp, 1)
}
