package stripe

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
)

// manualInvoiceJSON is a paid manual top-up invoice of org as Stripe serves it with payments
// expanded: €100 net, €121 with VAT, all of it due (no customer balance applied), paid through
// pi_manual (or out of band when pi is empty).
func manualInvoiceJSON(id, pi, status, currency string, net int64, org common.Address) string {
	return manualInvoiceJSONDue(id, pi, status, currency, net, 12_100, org)
}

// manualInvoiceJSONDue is manualInvoiceJSON with amount_due set apart from the €121 total.
func manualInvoiceJSONDue(id, pi, status, currency string, net, due int64, org common.Address) string {
	payments := `[]`
	if pi != "" {
		payments = `[{"object":"invoice_payment","status":"paid","payment":{"type":"payment_intent",` +
			`"payment_intent":{"id":"` + pi + `","object":"payment_intent"}}}]`
	}
	return `{"id":"` + id + `","object":"invoice","status":"` + status + `","currency":"` + currency + `",` +
		`"number":"VOC-0001","total":12100,"amount_due":` + jsonInt(due) + `,"starting_balance":` + jsonInt(due-12_100) + `,` +
		`"total_excluding_tax":` + jsonInt(net) + `,` +
		`"invoice_pdf":"https://pay.stripe.com/invoice/acct_1/live_abc/pdf?s=ap",` +
		`"metadata":{"` + MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `","` +
		MetadataKeyWalletTopUpSource + `":"` + TopUpSourceManual + `"},` +
		`"payments":{"object":"list","data":` + payments + `}}`
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// stubInvoices serves GET /v1/invoices/{id} from invoices (404 otherwise), counting fetches.
func stubInvoices(t *testing.T, invoices map[string]string) *int {
	t.Helper()
	fetches := new(int)
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := invoices[path.Base(r.URL.Path)]
		if !strings.HasPrefix(r.URL.Path, "/v1/invoices/") || !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
			return
		}
		*fetches++
		_, _ = w.Write([]byte(body))
	}))
	return fetches
}

// TestHandleInvoicePaid: a paid manual top-up invoice credits its net (total_excluding_tax) once,
// keyed by the invoice, with the payment intent that paid it; replays credit nothing. Checkout and
// subscription invoices are ignored without a fetch, and an invoice that can never be credited (not
// eur, a managed or unknown organization, a customer credit balance applied so less than the
// total was due, or re-targeted to another organization after the event) is dropped as permanent
// with nothing credited; a debit balance paid on top of the total still credits the net, with the
// amount collected as the gross so a refund mirrors exactly the net; one that nets nothing credits
// nothing; one Stripe cannot serve is retried.
func TestHandleInvoicePaid(t *testing.T) {
	c := qt.New(t)
	ms := newTestStorage(t)
	s := &Service{db: ms}
	org, outOfBand, managed, unknown := common.Address{0x21}, common.Address{0x22}, common.Address{0x23}, common.Address{0x24}
	for _, o := range []*db.Organization{
		{Address: org, Type: db.CompanyType},
		{Address: outOfBand, Type: db.CompanyType},
		{Address: managed, Type: db.CompanyType, ManagedBy: org},
	} {
		c.Assert(ms.SetOrganization(o), qt.IsNil)
	}
	fetches := stubInvoices(t, map[string]string{
		"in_paid":     manualInvoiceJSON("in_paid", "pi_manual", "paid", "eur", 10_000, org),
		"in_oob":      manualInvoiceJSON("in_oob", "", "paid", "eur", 5_000, outOfBand),
		"in_open":     manualInvoiceJSON("in_open", "", "open", "eur", 10_000, org),
		"in_usd":      manualInvoiceJSON("in_usd", "pi_usd", "paid", "usd", 10_000, org),
		"in_zero":     manualInvoiceJSON("in_zero", "pi_zero", "paid", "eur", 0, org),
		"in_managed":  manualInvoiceJSON("in_managed", "pi_m", "paid", "eur", 10_000, managed),
		"in_unknown":  manualInvoiceJSON("in_unknown", "pi_u", "paid", "eur", 10_000, unknown),
		"in_balance":  manualInvoiceJSONDue("in_balance", "pi_b", "paid", "eur", 10_000, 10_000, org),
		"in_debit":    manualInvoiceJSONDue("in_debit", "pi_d", "paid", "eur", 10_000, 13_000, org),
		"in_moved":    manualInvoiceJSON("in_moved", "pi_mv", "paid", "eur", 10_000, outOfBand),
		"in_checkout": `{"id":"in_checkout","object":"invoice","status":"paid"}`,
	})
	// eventFor is Stripe's invoice.paid payload: the invoice as it was when the event fired
	eventFor := func(id, source string, o common.Address) error {
		raw := `{"id":"` + id + `","object":"invoice","status":"paid","metadata":{"` +
			MetadataKeyWalletTopUpSource + `":"` + source + `","` + MetadataKeyWalletTopUpOrg + `":"` + o.Hex() + `"}}`
		return s.HandleEvent(&stripeapi.Event{
			ID: "evt_" + id, Type: stripeapi.EventTypeInvoicePaid,
			Data: &stripeapi.EventData{Raw: json.RawMessage(raw)},
		})
	}
	// event is eventFor with the payload naming the organization the live invoice names
	liveOrg := map[string]common.Address{"in_oob": outOfBand, "in_managed": managed, "in_unknown": unknown}
	event := func(id, source string) error {
		o, ok := liveOrg[id]
		if !ok {
			o = org
		}
		return eventFor(id, source, o)
	}
	balance := func(o common.Address) pricing.Cents {
		wallet, err := ms.Wallet(o)
		c.Assert(err, qt.IsNil)
		return wallet.BalanceCents
	}

	// paid through Stripe, delivered twice: one row, with the payment intent a refund names
	c.Assert(event("in_paid", TopUpSourceManual), qt.IsNil)
	c.Assert(event("in_paid", TopUpSourceManual), qt.IsNil)
	c.Assert(balance(org), qt.Equals, pricing.Cents(10_000))
	entry, err := ms.WalletEntry(org, InvoiceTopUpKey("in_paid"))
	c.Assert(err, qt.IsNil)
	c.Assert(entry.Kind, qt.Equals, db.WalletEntryTopUp)
	c.Assert(entry.PaymentIntentID, qt.Equals, "pi_manual")
	c.Assert(entry.GrossCents, qt.Equals, pricing.Cents(12_100))
	topUp, err := ms.WalletTopUpByPaymentIntent("pi_manual")
	c.Assert(err, qt.IsNil)
	c.Assert(topUp.ID, qt.Equals, entry.ID)

	// a debit balance (€9 owed) paid on top of the total: the total was collected, the net is credited,
	// and the gross is what the payment collected (amount_due), so a full refund of it debits the net
	c.Assert(event("in_debit", TopUpSourceManual), qt.IsNil)
	c.Assert(balance(org), qt.Equals, pricing.Cents(20_000))
	entry, err = ms.WalletEntry(org, InvoiceTopUpKey("in_debit"))
	c.Assert(err, qt.IsNil)
	c.Assert(entry.PaymentIntentID, qt.Equals, "pi_d")
	c.Assert(entry.GrossCents, qt.Equals, pricing.Cents(13_000))
	c.Assert(s.debitTopUpRefund(&stripeapi.PaymentIntent{ID: "pi_d"}, 13_000, "striperefund:re_d"), qt.IsNil)
	c.Assert(balance(org), qt.Equals, pricing.Cents(10_000))

	// paid out of band: credited, no payment intent
	c.Assert(event("in_oob", TopUpSourceManual), qt.IsNil)
	c.Assert(balance(outOfBand), qt.Equals, pricing.Cents(5_000))
	entry, err = ms.WalletEntry(outOfBand, InvoiceTopUpKey("in_oob"))
	c.Assert(err, qt.IsNil)
	c.Assert(entry.PaymentIntentID, qt.Equals, "")

	// not a manual invoice: ignored without touching Stripe
	before := *fetches
	c.Assert(event("in_checkout", TopUpSourceCheckout), qt.IsNil)
	c.Assert(event("in_checkout", ""), qt.IsNil)
	c.Assert(*fetches, qt.Equals, before)

	// live state rules: not paid anymore, or nets nothing, credits nothing and is done
	c.Assert(event("in_open", TopUpSourceManual), qt.IsNil)
	c.Assert(event("in_zero", TopUpSourceManual), qt.IsNil)
	// can never be credited: dropped as permanent
	for _, id := range []string{"in_usd", "in_managed", "in_unknown", "in_balance"} {
		err := event(id, TopUpSourceManual)
		c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue, qt.Commentf("%s: %v", id, err))
	}
	// its wallet_topup_org was edited after the event fired: neither organization is credited
	err = eventFor("in_moved", TopUpSourceManual, org)
	c.Assert(err, qt.ErrorMatches, "invoice in_moved was issued for organization .*")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue)
	// a payload naming no valid organization: dropped as permanent, naming what it carried
	err = s.HandleEvent(&stripeapi.Event{
		ID: "evt_noorg", Type: stripeapi.EventTypeInvoicePaid,
		Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"in_paid2","object":"invoice","metadata":{"` +
			MetadataKeyWalletTopUpSource + `":"` + TopUpSourceManual + `","` + MetadataKeyWalletTopUpOrg + `":"0x12"}}`)},
	})
	c.Assert(err, qt.ErrorMatches, `invalid organization address "0x12" in the payload of invoice in_paid2.*`)
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue)
	c.Assert(balance(org), qt.Equals, pricing.Cents(10_000))
	c.Assert(balance(outOfBand), qt.Equals, pricing.Cents(5_000))
	c.Assert(balance(managed), qt.Equals, pricing.Cents(0))
	_, err = ms.WalletEntry(org, InvoiceTopUpKey("in_balance"))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	// Stripe cannot serve it: retried
	err = event("in_gone", TopUpSourceManual)
	c.Assert(err, qt.ErrorMatches, "failed to re-read invoice in_gone.*")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsFalse)
}

// TestManualInvoiceRefundBeforePaid: a refund of the payment of a manual top-up invoice handled
// before its invoice.paid event credits the invoice first, under the invoice's key, then debits
// it; invoice.paid credits nothing more when it lands, so the two net to zero, across replays. A
// refund of a payment that paid a checkout's invoice, or no invoice at all, is ignored here, and
// one of a manual invoice whose customer is not stamped with the organization it names (the
// invoice was re-targeted) is dropped as permanent with nothing credited.
func TestManualInvoiceRefundBeforePaid(t *testing.T) {
	c := qt.New(t)
	ms := newTestStorage(t)
	s := &Service{db: ms}
	org, moved := common.Address{0x31}, common.Address{0x32}
	for _, o := range []common.Address{org, moved} {
		c.Assert(ms.SetOrganization(&db.Organization{Address: o, Type: db.CompanyType}), qt.IsNil)
	}
	// payment intent -> invoice
	paidBy := map[string]string{
		"pi_manual": "in_manual", "pi_checkout": "in_checkout", "pi_moved": "in_moved", "pi_nocustomer": "in_nocustomer",
		"pi_cusbroken": "in_cusbroken",
	}
	// billedTo is a manual invoice of o paid by pi and billed to customer
	billedTo := func(id, pi, customer string, o common.Address) string {
		return `{"customer":"` + customer + `",` + manualInvoiceJSON(id, pi, "paid", "eur", 10_000, o)[1:]
	}
	stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/refunds/"):
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"refund","status":"succeeded"}`))
		case r.URL.Path == "/v1/checkout/sessions":
			// no checkout session made these payments
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/checkout/sessions","has_more":false,"data":[]}`))
		case r.URL.Path == "/v1/invoice_payments":
			c.Check(r.URL.Query().Get("payment[type]"), qt.Equals, "payment_intent")
			c.Check(r.URL.Query().Get("status"), qt.Equals, "paid")
			if r.URL.Query().Get("payment[payment_intent]") == "pi_broken" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
				return
			}
			data := "[]"
			if invoice, ok := paidBy[r.URL.Query().Get("payment[payment_intent]")]; ok {
				data = `[{"id":"inpay_1","object":"invoice_payment","status":"paid","invoice":"` + invoice + `"}]`
			}
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/invoice_payments","has_more":false,"data":` + data + `}`))
		case r.URL.Path == "/v1/invoices/in_manual":
			_, _ = w.Write([]byte(billedTo("in_manual", "pi_manual", "cus_org", org)))
		case r.URL.Path == "/v1/invoices/in_moved":
			// re-targeted to moved after it was paid: its customer is still org's
			_, _ = w.Write([]byte(billedTo("in_moved", "pi_moved", "cus_org", moved)))
		case r.URL.Path == "/v1/invoices/in_nocustomer":
			_, _ = w.Write([]byte(manualInvoiceJSON("in_nocustomer", "pi_nocustomer", "paid", "eur", 10_000, org)))
		case r.URL.Path == "/v1/invoices/in_cusbroken":
			_, _ = w.Write([]byte(billedTo("in_cusbroken", "pi_cusbroken", "cus_broken", org)))
		case r.URL.Path == "/v1/customers/cus_org":
			_, _ = w.Write([]byte(`{"id":"cus_org","object":"customer","metadata":{"address":"` + org.String() + `"}}`))
		case r.URL.Path == "/v1/customers/cus_broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case r.URL.Path == "/v1/invoices/in_checkout":
			_, _ = w.Write([]byte(`{"id":"in_checkout","object":"invoice","status":"paid","metadata":{"` +
				MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `","` + MetadataKeyWalletTopUpSource + `":"` + TopUpSourceCheckout + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		}
	}))
	refund := func(id, paymentIntent string) error {
		return s.HandleEvent(&stripeapi.Event{
			ID: "evt_" + id, Type: stripeapi.EventTypeRefundCreated,
			Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"` + id + `","object":"refund",` +
				`"status":"succeeded","amount":12100,"payment_intent":"` + paymentIntent + `"}`)},
		})
	}
	paid := func() error {
		return s.HandleEvent(&stripeapi.Event{
			ID: "evt_in_manual", Type: stripeapi.EventTypeInvoicePaid,
			Data: &stripeapi.EventData{Raw: json.RawMessage(`{"id":"in_manual","object":"invoice","status":"paid","metadata":{"` +
				MetadataKeyWalletTopUpSource + `":"` + TopUpSourceManual + `","` + MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `"}}`)},
		})
	}

	// not a manual top-up: nothing is credited or debited
	c.Assert(refund("re_checkout", "pi_checkout"), qt.IsNil)
	c.Assert(refund("re_other", "pi_other"), qt.IsNil)
	total, _, err := ms.WalletLedger(org, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(0))

	// the invoice payments cannot be listed: the refund is retried, not dropped
	err = refund("re_broken", "pi_broken")
	c.Assert(err, qt.ErrorMatches, ".*failed to list invoice payments.*")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsFalse)

	// the invoice's customer is not stamped with the organization the invoice names (or it has
	// none): dropped as permanent, nothing credited to either; a customer that cannot be read is retried
	err = refund("re_moved", "pi_moved")
	c.Assert(err, qt.ErrorMatches, ".*invoice in_moved names organization .* but its customer cus_org carries .*")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue)
	err = refund("re_nocustomer", "pi_nocustomer")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsTrue, qt.Commentf("%v", err))
	err = refund("re_cusbroken", "pi_cusbroken")
	c.Assert(err, qt.ErrorMatches, ".*failed to read customer cus_broken of invoice in_cusbroken.*")
	c.Assert(errors.Is(err, errPermanentEvent), qt.IsFalse)
	for _, o := range []common.Address{org, moved} {
		total, _, err := ms.WalletLedger(o, 1, 10)
		c.Assert(err, qt.IsNil)
		c.Assert(total, qt.Equals, int64(0))
	}

	// the refund first, then invoice.paid, each delivered twice
	c.Assert(refund("re_manual", "pi_manual"), qt.IsNil)
	c.Assert(refund("re_manual", "pi_manual"), qt.IsNil)
	c.Assert(paid(), qt.IsNil)
	c.Assert(paid(), qt.IsNil)
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
	c.Assert(entries[1].IdempotencyKey, qt.Equals, InvoiceTopUpKey("in_manual"))
}

// TestTopUpInvoicePDF: a checkout top-up's key resolves its invoice through the session, a manual
// top-up's names it; the PDF is fetched from Stripe (following its redirect) with the invoice
// number as its name. No invoice, no PDF, or a key Stripe knows no session by, is ErrNoInvoice; a
// PDF URL off stripe.com is refused, and a session Stripe fails to serve is a Stripe error.
func TestTopUpInvoicePDF(t *testing.T) {
	c := qt.New(t)
	const pdf = "%PDF-1.4 stub"
	srv := stubStripeAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkout/sessions/cs_1":
			_, _ = w.Write([]byte(`{"id":"cs_1","object":"checkout.session","invoice":"in_1"}`))
		case "/v1/checkout/sessions/cs_free":
			_, _ = w.Write([]byte(`{"id":"cs_free","object":"checkout.session","invoice":null}`))
		case "/v1/checkout/sessions/cs_broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case "/v1/invoices/in_1":
			_, _ = w.Write([]byte(`{"id":"in_1","object":"invoice","number":"VOC-0001",` +
				`"invoice_pdf":"https://pay.stripe.com/invoice/acct_1/live_abc/pdf?s=ap"}`))
		case "/v1/invoices/in_draft":
			_, _ = w.Write([]byte(`{"id":"in_draft","object":"invoice"}`))
		case "/v1/invoices/in_evil":
			_, _ = w.Write([]byte(`{"id":"in_evil","object":"invoice","number":"X","invoice_pdf":"https://evil.example/x.pdf"}`))
		case "/invoice/acct_1/live_abc/pdf":
			// pay.stripe.com redirects to the file
			http.Redirect(w, r, "https://files.stripe.com/invoice.pdf", http.StatusFound)
		case "/invoice.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte(pdf))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		}
	}))
	// the PDF client keeps its host check; its requests are carried to the stub instead
	stubURL, err := url.Parse(srv.URL)
	c.Assert(err, qt.IsNil)
	previous := invoicePDFClient.Transport
	invoicePDFClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = stubURL.Scheme, stubURL.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	c.Cleanup(func() { invoicePDFClient.Transport = previous })
	s := &Service{client: NewClient(&Config{APIKey: "sk_test_stub"})}

	for _, key := range []string{"cs_1", InvoiceTopUpKey("in_1")} {
		got, err := s.TopUpInvoicePDF(key)
		c.Assert(err, qt.IsNil, qt.Commentf("key %s", key))
		body, err := io.ReadAll(got.Body)
		c.Assert(err, qt.IsNil)
		c.Assert(got.Body.Close(), qt.IsNil)
		c.Assert(got.Number, qt.Equals, "VOC-0001")
		c.Assert(string(body), qt.Equals, pdf)
	}
	// cs_unknown is no checkout session for Stripe (resource_missing)
	for _, key := range []string{"cs_free", "cs_unknown", InvoiceTopUpKey("in_draft")} {
		_, err := s.TopUpInvoicePDF(key)
		c.Assert(err, qt.ErrorIs, ErrNoInvoice, qt.Commentf("key %s", key))
	}
	_, err = s.TopUpInvoicePDF(InvoiceTopUpKey("in_evil"))
	c.Assert(err, qt.ErrorMatches, ".*refusing to fetch an invoice pdf.*")
	_, err = s.TopUpInvoicePDF("cs_broken")
	c.Assert(err, qt.ErrorMatches, ".*failed to get checkout session.*")
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
