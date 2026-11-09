package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// refundObject is the wire shape of a refund inside a webhook event.
func refundObject(id, paymentIntentID, status string, amountCents pricing.Cents) map[string]any {
	return map[string]any{
		"id": id, "object": "refund", "amount": amountCents, "status": status, "payment_intent": paymentIntentID,
	}
}

// stubStripeRefunds answers the refund re-read the webhook does for a succeeded refund: with the
// status stored for its id in the returned map, succeeded when none is. An object stored for an id
// (a dispute) is answered as it is. A payment intent with no credited top-up has no top-up
// checkout session either.
func stubStripeRefunds(t *testing.T) *sync.Map {
	t.Helper()
	statuses := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/checkout/sessions" {
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/checkout/sessions","has_more":false,"data":[]}`))
			return
		}
		id := path.Base(r.URL.Path)
		if stored, ok := statuses.Load(id); ok {
			if obj, isObject := stored.(map[string]any); isObject {
				_ = json.NewEncoder(w).Encode(obj) // a stored object, such as a dispute, as it stands now
				return
			}
		}
		status := "succeeded"
		if s, ok := statuses.Load(id); ok {
			if stored, isString := s.(string); isString {
				status = stored
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "refund", "status": status})
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	return statuses
}

// TestStripeRefundWebhookDebitsWallet: a refund of a top-up made by hand in the Stripe dashboard
// is mirrored in the wallet: debited net of the top-up's own VAT, once per refund across replays
// and its later updates, a partial refund and the rest each once, even below zero, and shown in
// the history as a refund. Refunds that are not settled, or of payments that are not top-ups,
// change nothing; a debited refund that later fails or is canceled is credited back once.
func TestStripeRefundWebhookDebitsWallet(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	refunds := stubStripeRefunds(t)
	token := testCreateUser(t, "refundsync123456")
	orgAddress := testCreateOrganization(t, token)

	// a €100 top-up charged €121 with VAT, then €80 of it spent
	intent := "pi_refund_" + util.RandomHex(6)
	topUp := checkoutSessionObject("cs_refund_"+util.RandomHex(6), "paid", 10_000, map[string]string{
		stripe.MetadataKeyWalletTopUpOrg: orgAddress.String(),
	})
	topUp["amount_total"] = 12_100
	topUp["payment_intent"] = intent
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", topUp)
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: bson.NewObjectID(), ProcessOrgAddress: orgAddress,
		AmountCents: 8_000, PriceCents: 8_000,
	}), qt.IsNil)

	post := func(eventID, eventType string, refund map[string]any) {
		c.Assert(postSignedStripeEvent(t, testWebhookSecret, eventID, eventType, refund), qt.Equals, http.StatusOK)
	}
	// not settled yet, or never: nothing moves
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_partial", intent, "pending", 3_025))
	post("evt_"+util.RandomHex(8), "refund.updated", refundObject("re_failed", intent, "failed", 3_025))
	assertWallet(t, orgAddress, 2_000, db.WalletEntryRefund, 0)

	// a partial refund of €30.25 gross is €25 net: debited once across the updated event and a
	// fresh-id replay
	partialEvent := "evt_" + util.RandomHex(8)
	post(partialEvent, "refund.updated", refundObject("re_partial", intent, "succeeded", 3_025))
	post(partialEvent, "refund.updated", refundObject("re_partial", intent, "succeeded", 3_025))
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_partial", intent, "succeeded", 3_025))
	assertWallet(t, orgAddress, -500, db.WalletEntryRefund, 1)

	// the rest (€90.75 gross, €75 net) lands in full, deeper below zero
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_rest", intent, "succeeded", 9_075))
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_rest", intent, "succeeded", 9_075))
	assertWallet(t, orgAddress, -8_000, db.WalletEntryRefund, 2)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryRefund)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(-7_500))

	// a refund of a payment that never credited a wallet (a legacy subscription invoice) is ignored
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_other", "pi_not_a_topup", "succeeded", 5_000))
	assertWallet(t, orgAddress, -8_000, db.WalletEntryRefund, 2)

	// in debt, the wallet pays nothing until a top-up covers it
	err = testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: bson.NewObjectID(), AmountCents: 1, PriceCents: 1,
	})
	c.Assert(err, qt.ErrorIs, db.ErrInsufficientWalletBalance)

	// a debited refund that later fails is credited back once, as a refund row
	post("evt_"+util.RandomHex(8), "refund.updated", refundObject("re_rest", intent, "failed", 9_075))
	post("evt_"+util.RandomHex(8), "refund.updated", refundObject("re_rest", intent, "canceled", 9_075))
	assertWallet(t, orgAddress, -500, db.WalletEntryRefund, 3)

	// out of order: a refund whose failed update is handled before its succeeded event has nothing
	// to reverse, and the succeeded event, checked against Stripe, debits nothing either
	refunds.Store("re_late", "failed")
	post("evt_"+util.RandomHex(8), "refund.updated", refundObject("re_late", intent, "failed", 1_210))
	post("evt_"+util.RandomHex(8), "refund.created", refundObject("re_late", intent, "succeeded", 1_210))
	assertWallet(t, orgAddress, -500, db.WalletEntryRefund, 3)
}

// disputeObject is the wire shape of a dispute inside a webhook event.
func disputeObject(id, paymentIntentID string, amountCents pricing.Cents) map[string]any {
	return map[string]any{"id": id, "object": "dispute", "amount": amountCents, "payment_intent": paymentIntentID}
}

// TestStripeDisputeWebhookDebitsWallet: a dispute on a top-up debits the withdrawn funds net of
// VAT, once, even below zero; reinstated funds credit that debit back once. A dispute on a payment
// that is not a top-up, or reinstated funds never debited, change nothing.
func TestStripeDisputeWebhookDebitsWallet(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	disputes := stubStripeRefunds(t)
	token := testCreateUser(t, "disputesync123456")
	orgAddress := testCreateOrganization(t, token)

	// a €100 top-up charged €121 with VAT, then €80 of it spent
	intent := "pi_dispute_" + util.RandomHex(6)
	topUp := checkoutSessionObject("cs_dispute_"+util.RandomHex(6), "paid", 10_000, map[string]string{
		stripe.MetadataKeyWalletTopUpOrg: orgAddress.String(),
	})
	topUp["amount_total"] = 12_100
	topUp["payment_intent"] = intent
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", topUp)
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: bson.NewObjectID(), ProcessOrgAddress: orgAddress,
		AmountCents: 8_000, PriceCents: 8_000,
	}), qt.IsNil)

	post := func(eventType string, dispute map[string]any) {
		// the handler re-reads the dispute from Stripe: it reads it as the event carries it
		disputes.Store(dispute["id"], dispute)
		c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), eventType, dispute),
			qt.Equals, http.StatusOK)
	}
	dispute := "dp_" + util.RandomHex(6)
	// the dispute as Stripe answers its re-read: its balance transactions list every movement of
	// its funds so far
	withMoves := func(amounts ...int64) map[string]any {
		obj := disputeObject(dispute, intent, 12_100)
		moves := make([]map[string]any, 0, len(amounts))
		for i, amount := range amounts {
			moves = append(moves, map[string]any{
				"id": fmt.Sprintf("txn_%s_%d", dispute, i), "object": "balance_transaction", "amount": amount,
			})
		}
		obj["balance_transactions"] = moves
		return obj
	}
	post("charge.dispute.funds_reinstated", withMoves()) // never withdrawn
	post("charge.dispute.funds_withdrawn", disputeObject("dp_other", "pi_not_a_topup", 5_000))
	assertWallet(t, orgAddress, 2_000, db.WalletEntryRefund, 0)

	// the whole €121 is withdrawn: €100 net, below zero. The first read may list no movement yet:
	// the event itself is the first withdrawal
	post("charge.dispute.funds_withdrawn", withMoves())
	post("charge.dispute.funds_withdrawn", withMoves(-12_100))
	assertWallet(t, orgAddress, -8_000, db.WalletEntryRefund, 1)

	// the dispute is won: the funds come back once, even when the event carries more than 64 KB
	// of evidence
	won := withMoves(-12_100, 12_100)
	won["evidence"] = map[string]any{"uncategorized_text": strings.Repeat("evidence ", 8_000)}
	post("charge.dispute.funds_reinstated", won)
	post("charge.dispute.funds_reinstated", won)
	assertWallet(t, orgAddress, 2_000, db.WalletEntryRefund, 2)

	// the network takes the funds again on the same dispute: debited again, once, and the next
	// reinstatement credits that second debit back
	post("charge.dispute.funds_withdrawn", withMoves(-12_100, 12_100, -12_100))
	post("charge.dispute.funds_withdrawn", withMoves(-12_100, 12_100, -12_100))
	assertWallet(t, orgAddress, -8_000, db.WalletEntryRefund, 3)
	post("charge.dispute.funds_reinstated", withMoves(-12_100, 12_100, -12_100, 12_100))
	assertWallet(t, orgAddress, 2_000, db.WalletEntryRefund, 4)
}
