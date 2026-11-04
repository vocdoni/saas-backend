package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/test"
)

// topUpSessionJSON is a paid €100 top-up of org charged €121 with VAT, as Stripe lists it.
func topUpSessionJSON(id, paymentIntent string, org common.Address) string {
	return `{"id":"` + id + `","object":"checkout.session","payment_status":"paid","amount_subtotal":10000,` +
		`"amount_total":12100,"payment_intent":"` + paymentIntent + `","metadata":{"` +
		MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `"}}`
}

// stubTopUpStripe answers a refund re-read as succeeded, a dispute re-read with what disputes
// holds for its id (404 when nothing), and the checkout session list of a payment intent with the
// sessions given for it (none for any other).
func stubTopUpStripe(t *testing.T, sessions map[string]string, disputes *sync.Map) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/disputes/") {
			dispute, ok := disputes.Load(path.Base(r.URL.Path))
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error"}}`))
				return
			}
			_, _ = w.Write([]byte(dispute.(string)))
			return
		}
		if r.URL.Path == "/v1/checkout/sessions" {
			data := "[]"
			if session, ok := sessions[r.URL.Query().Get("payment_intent")]; ok {
				data = "[" + session + "]"
			}
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/checkout/sessions","has_more":false,"data":` + data + `}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": path.Base(r.URL.Path), "object": "refund", "status": "succeeded"})
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

// TestTopUpRefundBeforeCredit: a full refund of a top-up nets the wallet to zero whether its event
// is handled before or after the top-up's completed event, across replays of both. Handled first,
// the refund credits the top-up from its checkout session under the session's key, so the
// completed event credits nothing. A refund of a payment no top-up session made is ignored.
func TestTopUpRefundBeforeCredit(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	ms, err := db.New(uri, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)
	s := &Service{db: ms}

	refundFirst, creditFirst := common.Address{0x01}, common.Address{0x02}
	stubTopUpStripe(t, map[string]string{
		"pi_refund_first": topUpSessionJSON("cs_refund_first", "pi_refund_first", refundFirst),
		"pi_credit_first": topUpSessionJSON("cs_credit_first", "pi_credit_first", creditFirst),
	}, &sync.Map{})
	refund := func(id, paymentIntent string) error {
		return s.handleRefund(&stripeapi.Event{
			ID: "evt_" + id, Type: stripeapi.EventTypeRefundCreated,
			Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"` + id + `","object":"refund",` +
				`"status":"succeeded","amount":12100,"payment_intent":"` + paymentIntent + `"}`)},
		})
	}
	completed := func(id, paymentIntent string, org common.Address) error {
		var session stripeapi.CheckoutSession
		c.Assert(json.Unmarshal([]byte(topUpSessionJSON(id, paymentIntent, org)), &session), qt.IsNil)
		return s.creditWalletTopUp(&session)
	}
	assertNetZero := func(org common.Address) {
		c.Helper()
		wallet, err := ms.Wallet(org)
		c.Assert(err, qt.IsNil)
		c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
		total, entries, err := ms.WalletLedger(org, 1, 10)
		c.Assert(err, qt.IsNil)
		c.Assert(total, qt.Equals, int64(2))
		c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryRefund)
		c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(-10_000))
		c.Assert(entries[1].Kind, qt.Equals, db.WalletEntryTopUp)
		c.Assert(entries[1].AmountCents, qt.Equals, pricing.Cents(10_000))
	}

	// refund handled first, then the completed event, each delivered twice
	c.Assert(refund("re_refund_first", "pi_refund_first"), qt.IsNil)
	c.Assert(refund("re_refund_first", "pi_refund_first"), qt.IsNil)
	c.Assert(completed("cs_refund_first", "pi_refund_first", refundFirst), qt.IsNil)
	c.Assert(completed("cs_refund_first", "pi_refund_first", refundFirst), qt.IsNil)
	assertNetZero(refundFirst)

	// the usual order
	c.Assert(completed("cs_credit_first", "pi_credit_first", creditFirst), qt.IsNil)
	c.Assert(refund("re_credit_first", "pi_credit_first"), qt.IsNil)
	c.Assert(completed("cs_credit_first", "pi_credit_first", creditFirst), qt.IsNil)
	c.Assert(refund("re_credit_first", "pi_credit_first"), qt.IsNil)
	assertNetZero(creditFirst)

	// not a top-up: nothing is credited or debited
	c.Assert(refund("re_invoice", "pi_invoice"), qt.IsNil)
	_, err = ms.WalletTopUpByPaymentIntent("pi_invoice")
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}

// TestTopUpDisputeAnyOrder: a dispute's movements end the same whatever order their events are
// handled in, across replays: each event re-reads the dispute and applies what its balance
// transactions list now. A reinstatement handled before the withdrawal it undoes leaves nothing
// debited when the withdrawal lands; withdrawn again after a reinstatement debits again.
func TestTopUpDisputeAnyOrder(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	ms, err := db.New(uri, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)
	s := &Service{db: ms}

	reinstatedFirst, withdrawnFirst := common.Address{0x03}, common.Address{0x04}
	disputes := &sync.Map{}
	stubTopUpStripe(t, map[string]string{
		"pi_reinstated_first": topUpSessionJSON("cs_reinstated_first", "pi_reinstated_first", reinstatedFirst),
		"pi_withdrawn_first":  topUpSessionJSON("cs_withdrawn_first", "pi_withdrawn_first", withdrawnFirst),
	}, disputes)
	// setDispute makes Stripe list the dispute id on paymentIntent with these fund movements
	setDispute := func(id, paymentIntent string, moves ...int64) {
		var bts []string
		for _, m := range moves {
			bts = append(bts, `{"object":"balance_transaction","amount":`+strconv.FormatInt(m, 10)+`}`)
		}
		disputes.Store(id, `{"id":"`+id+`","object":"dispute","amount":12100,"payment_intent":"`+paymentIntent+
			`","balance_transactions":[`+strings.Join(bts, ",")+`]}`)
	}
	// event delivers the dispute as it was when the event was sent: only id and type matter
	event := func(id string, eventType stripeapi.EventType) error {
		return s.handleDispute(&stripeapi.Event{
			ID: "evt_" + id, Type: eventType,
			Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"` + id + `","object":"dispute"}`)},
		})
	}
	withdrawn, reinstated := stripeapi.EventTypeChargeDisputeFundsWithdrawn, stripeapi.EventTypeChargeDisputeFundsReinstated
	assertWallet := func(org common.Address, balance pricing.Cents, rows int64) {
		c.Helper()
		wallet, err := ms.Wallet(org)
		c.Assert(err, qt.IsNil)
		c.Assert(wallet.BalanceCents, qt.Equals, balance)
		total, _, err := ms.WalletLedger(org, 1, 10)
		c.Assert(err, qt.IsNil)
		c.Assert(total, qt.Equals, rows)
	}

	// the withdrawal's delivery failed; Stripe reinstated the funds before its retry lands
	setDispute("dp_reinstated_first", "pi_reinstated_first", -12100, 12100)
	c.Assert(event("dp_reinstated_first", reinstated), qt.IsNil)
	c.Assert(event("dp_reinstated_first", withdrawn), qt.IsNil)
	c.Assert(event("dp_reinstated_first", reinstated), qt.IsNil)
	assertWallet(reinstatedFirst, 10_000, 1) // the top-up alone: nothing debited

	// the usual order, each event replayed
	setDispute("dp_withdrawn_first", "pi_withdrawn_first", -12100)
	c.Assert(event("dp_withdrawn_first", withdrawn), qt.IsNil)
	c.Assert(event("dp_withdrawn_first", withdrawn), qt.IsNil)
	assertWallet(withdrawnFirst, 0, 2)
	setDispute("dp_withdrawn_first", "pi_withdrawn_first", -12100, 12100)
	c.Assert(event("dp_withdrawn_first", reinstated), qt.IsNil)
	c.Assert(event("dp_withdrawn_first", withdrawn), qt.IsNil)
	assertWallet(withdrawnFirst, 10_000, 3)
	// withdrawn again (pre-arbitration): a second debit
	setDispute("dp_withdrawn_first", "pi_withdrawn_first", -12100, 12100, -12100)
	c.Assert(event("dp_withdrawn_first", withdrawn), qt.IsNil)
	c.Assert(event("dp_withdrawn_first", reinstated), qt.IsNil)
	assertWallet(withdrawnFirst, 0, 4)

	// a dispute that cannot be re-read is retried
	c.Assert(event("dp_unknown", withdrawn), qt.ErrorMatches, "failed to re-read dispute dp_unknown.*")
}
