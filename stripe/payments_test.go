package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/test"
)

// newTestStorage starts a MongoDB container for one test and opens a fresh database on it.
func newTestStorage(t *testing.T) *db.MongoStorage {
	t.Helper()
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
	return ms
}

// stubStripeAPI points the Stripe client at handler for one test, with no network retries.
func stubStripeAPI(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	return srv
}

// TestBuildPaymentSessionParams pins the wire shape of a one-time checkout session: mode
// payment, embedded UI, tax exclusive EUR price_data, invoice creation, and the routing
// metadata on the session itself (a payment session has no SubscriptionData) and on its payment
// intent, which a refund made in the dashboard names.
func TestBuildPaymentSessionParams(t *testing.T) {
	c := qt.New(t)

	params, err := buildPaymentSessionParams(&PaymentSessionParams{
		LineItems: []PaymentLineItem{
			{Description: "voting process (600 eligible voters)", AmountCents: 29_000},
			{Description: "email 2FA", AmountCents: 600},
		},
		Metadata: map[string]string{
			MetadataKeyWalletTopUpOrg: "0x000000000000000000000000000000000000bEEF",
			MetadataKeyProcessID:      "656e6f7567682062797465732100",
			MetadataKeyRequestedBy:    "admin@example.com",
		},
		ReturnURL: "https://app.example.com/payment",
		Locale:    "ca", // unsupported by Stripe, must fall back to es
	})
	c.Assert(err, qt.IsNil)

	c.Assert(*params.Mode, qt.Equals, "payment")
	c.Assert(*params.UIMode, qt.Equals, "elements")
	c.Assert(*params.AutomaticTax.Enabled, qt.IsTrue)
	c.Assert(*params.TaxIDCollection.Enabled, qt.IsTrue)
	c.Assert(*params.InvoiceCreation.Enabled, qt.IsTrue)
	// the invoice Checkout issues names its organization and its source: the invoice.paid
	// webhook credits manual invoices only, never a checkout's
	c.Assert(params.InvoiceCreation.InvoiceData.Metadata, qt.DeepEquals, map[string]string{
		MetadataKeyWalletTopUpOrg:    "0x000000000000000000000000000000000000bEEF",
		MetadataKeyWalletTopUpSource: TopUpSourceCheckout,
	})
	c.Assert(*params.BillingAddressCollection, qt.Equals, "auto")
	c.Assert(*params.Locale, qt.Equals, "es")
	c.Assert(*params.ReturnURL, qt.Equals, "https://app.example.com/payment/{CHECKOUT_SESSION_ID}")
	c.Assert(params.SubscriptionData, qt.IsNil)
	c.Assert(params.Metadata[MetadataKeyProcessID], qt.Equals, "656e6f7567682062797465732100")
	c.Assert(params.PaymentIntentData.Metadata[MetadataKeyWalletTopUpOrg], qt.Equals,
		"0x000000000000000000000000000000000000bEEF")

	c.Assert(params.LineItems, qt.HasLen, 2)
	for i, want := range []struct {
		name  string
		cents pricing.Cents
	}{
		{"voting process (600 eligible voters)", 29_000},
		{"email 2FA", 600},
	} {
		item := params.LineItems[i]
		c.Assert(*item.Quantity, qt.Equals, int64(1))
		c.Assert(*item.PriceData.Currency, qt.Equals, "eur")
		c.Assert(*item.PriceData.TaxBehavior, qt.Equals, "exclusive")
		c.Assert(*item.PriceData.UnitAmount, qt.Equals, int64(want.cents))
		c.Assert(*item.PriceData.ProductData.Name, qt.Equals, want.name)
	}
}

// TestCreatePaymentCheckoutSessionCustomer: the session reuses the customer stamped with the
// organization, falls back to the email only when the search finds none, and is not created at
// all when the search fails, so an outage never makes Checkout create a duplicate customer.
func TestCreatePaymentCheckoutSessionCustomer(t *testing.T) {
	c := qt.New(t)
	search := "" // what the customer search answers: a customer id, "" for none, "fail"
	var created []string
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/customers/search" && search == "fail":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case r.URL.Path == "/v1/customers/search":
			data := "[]"
			if search != "" {
				data = `[{"id":"` + search + `","object":"customer"}]`
			}
			_, _ = w.Write([]byte(`{"object":"search_result","url":"/v1/customers/search","has_more":false,"data":` + data + `}`))
		default:
			c.Assert(r.ParseForm(), qt.IsNil)
			created = append(created, r.PostForm.Get("customer")+"|"+r.PostForm.Get("customer_email"))
			_, _ = w.Write([]byte(`{"id":"cs_1","object":"checkout.session"}`))
		}
	}))
	client := NewClient(&Config{APIKey: "sk_test_stub"})
	params := &PaymentSessionParams{OrgAddress: common.Address{0x01}.String(), CustomerEmail: "admin@org.test"}

	_, err := client.CreatePaymentCheckoutSession(params)
	c.Assert(err, qt.IsNil)
	search = "cus_org"
	_, err = client.CreatePaymentCheckoutSession(params)
	c.Assert(err, qt.IsNil)
	search = "fail"
	_, err = client.CreatePaymentCheckoutSession(params)
	c.Assert(err, qt.ErrorMatches, ".*failed to search customers.*")
	c.Assert(created, qt.DeepEquals, []string{"|admin@org.test", "cus_org|"})
}

// stubRefund points the Stripe client at a stub that answers GET /v1/refunds/{id} with status,
// or with a 500 when status is empty.
func stubRefund(t *testing.T, status stripeapi.RefundStatus) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": path.Base(r.URL.Path), "object": "refund", "status": status,
		})
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

// TestHandleRefundRereadsSucceeded: a succeeded refund event is checked against the refund as
// Stripe has it now. One that failed or was canceled since (its failed update handled first, and
// finding nothing to reverse) debits nothing, and a refund that cannot be read is retried.
func TestHandleRefundRereadsSucceeded(t *testing.T) {
	c := qt.New(t)
	event := &stripeapi.Event{
		ID:   "evt_refund",
		Type: stripeapi.EventTypeRefundUpdated,
		Data: &stripeapi.EventData{Raw: json.RawMessage(
			`{"id":"re_undone","object":"refund","status":"succeeded","amount":1000,"payment_intent":"pi_topup"}`)},
	}

	// no debit is attempted: the service has no database, so one would panic
	for _, status := range []stripeapi.RefundStatus{stripeapi.RefundStatusFailed, stripeapi.RefundStatusCanceled} {
		stubRefund(t, status)
		c.Assert((&Service{}).handleRefund(event), qt.IsNil)
	}
	stubRefund(t, "")
	c.Assert((&Service{}).handleRefund(event), qt.ErrorMatches, "failed to re-read refund re_undone.*")
}

// TestCreditTopUpStampsCustomer: crediting a checkout top-up stamps the organization address on
// its customer when it has none, so later lookups by address find it; a customer already stamped
// is left alone, as is an unstamped one when another customer already carries the organization or
// the search for one fails, and a customer Stripe cannot serve neither fails nor blocks the credit.
func TestCreditTopUpStampsCustomer(t *testing.T) {
	c := qt.New(t)
	ms := newTestStorage(t)
	s := &Service{db: ms}

	stamped := map[string]string{} // customer id -> address stamped on it
	taken, searchBroken := common.Address{0x14}, common.Address{0x15}
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		switch {
		case id == "search" && strings.Contains(r.URL.Query().Get("query"), searchBroken.String()):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case id == "search":
			// another customer already carries taken; none carries any other organization
			data := "[]"
			if strings.Contains(r.URL.Query().Get("query"), taken.String()) {
				data = `[{"id":"cus_first","object":"customer","metadata":{"address":"` + taken.String() + `"}}]`
			}
			_, _ = w.Write([]byte(`{"object":"search_result","url":"/v1/customers/search","has_more":false,"data":` + data + `}`))
		case !strings.HasPrefix(r.URL.Path, "/v1/customers/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		case id == "cus_broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case r.Method == http.MethodPost:
			c.Assert(r.ParseForm(), qt.IsNil)
			stamped[id] = r.PostForm.Get("metadata[address]")
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"customer"}`))
		case id == "cus_other":
			_, _ = w.Write([]byte(`{"id":"cus_other","object":"customer",` +
				`"metadata":{"address":"0x00000000000000000000000000000000000000ee"}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"customer","metadata":{}}`))
		}
	}))

	credit := func(sessionID, customerID string, org common.Address) {
		c.Helper()
		var session stripeapi.CheckoutSession
		raw := `{"id":"` + sessionID + `","object":"checkout.session","payment_status":"paid","amount_subtotal":10000,` +
			`"amount_total":12100,"customer":"` + customerID + `","metadata":{"` + MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `"}}`
		c.Assert(json.Unmarshal([]byte(raw), &session), qt.IsNil)
		c.Assert(s.creditWalletTopUp(&session), qt.IsNil)
		wallet, err := ms.Wallet(org)
		c.Assert(err, qt.IsNil)
		c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000))
	}
	credit("cs_fresh", "cus_fresh", common.Address{0x11})
	credit("cs_other", "cus_other", common.Address{0x12})
	credit("cs_broken", "cus_broken", common.Address{0x13})
	credit("cs_second", "cus_second", taken)
	credit("cs_nosearch", "cus_nosearch", searchBroken)
	c.Assert(stamped, qt.DeepEquals, map[string]string{"cus_fresh": common.Address{0x11}.String()})
}

// TestCreditTopUpStoresPricing: crediting a top-up opened for a process stores the pricing
// breakdown of its session on the ledger row. A session without one, or with a malformed one,
// is credited all the same, in full and with no breakdown on its row: the breakdown is display
// data, and never decides whether or how much a payment credits.
func TestCreditTopUpStoresPricing(t *testing.T) {
	c := qt.New(t)
	ms := newTestStorage(t)
	s := &Service{db: ms}
	org := common.Address{0x21}

	credit := func(sessionID string, metadata map[string]string) *db.WalletLedgerEntry {
		c.Helper()
		metadata[MetadataKeyWalletTopUpOrg] = org.Hex()
		raw, err := json.Marshal(map[string]any{
			"id": sessionID, "object": "checkout.session", "payment_status": "paid",
			"amount_subtotal": 1_916, "amount_total": 2_318, "metadata": metadata,
		})
		c.Assert(err, qt.IsNil)
		var session stripeapi.CheckoutSession
		c.Assert(json.Unmarshal(raw, &session), qt.IsNil)
		c.Assert(s.creditTopUpSession(&session), qt.IsNil)
		entry, err := ms.WalletEntry(org, sessionID)
		c.Assert(err, qt.IsNil)
		c.Assert(entry.AmountCents, qt.Equals, pricing.Cents(1_916))
		return entry
	}
	half := len(testPricingJSON) / 2
	c.Assert(credit("cs_priced", map[string]string{"pricing_0": testPricingJSON}).Pricing, qt.DeepEquals, testPricing())
	c.Assert(credit("cs_chunked", map[string]string{
		"pricing_0": testPricingJSON[:half], "pricing_1": testPricingJSON[half:],
	}).Pricing, qt.DeepEquals, testPricing())
	c.Assert(credit("cs_plain", map[string]string{}).Pricing, qt.IsNil)
	// malformed: a missing chunk, and a breakdown claiming far more than was paid in another currency
	c.Assert(credit("cs_gap", map[string]string{
		"pricing_0": testPricingJSON[:half], "pricing_2": testPricingJSON[half:],
	}).Pricing, qt.IsNil)
	c.Assert(credit("cs_usd", map[string]string{
		"pricing_0": strings.Replace(testPricingJSON, `"eur"`, `"usd"`, 1),
	}).Pricing, qt.IsNil)

	// every one credited what was paid, net: the breakdown moved no money
	wallet, err := ms.Wallet(org)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(5*1_916))
	// and a replay rewrites nothing
	c.Assert(credit("cs_priced", map[string]string{}).Pricing, qt.DeepEquals, testPricing())
}
