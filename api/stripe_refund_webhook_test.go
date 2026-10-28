package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
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

// TestStripeRefundWebhookDebitsWallet: a refund of a top-up made by hand in the Stripe dashboard
// is mirrored in the wallet: debited net of the top-up's own VAT, once per refund across replays
// and its later updates, a partial refund and the rest each once, even below zero, and shown in
// the history as a refund. Refunds that are not settled, or of payments that are not top-ups,
// change nothing; a debited refund that later fails or is canceled is credited back once.
func TestStripeRefundWebhookDebitsWallet(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
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

	// a refund of a payment that never credited a wallet (a subscription invoice) is ignored
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
}
