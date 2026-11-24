package topup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/stripe"
)

// fakeInvoice is an invoice of deferredFake.
type fakeInvoice struct {
	ID, Status, Customer string
	Metadata             map[string]string
	Lines, Products      []string // line descriptions and product names
	Net                  int64
	Created              int64
	Deleted              bool
}

// deferredFake is a stateful Stripe account for IssueDeferredInvoice: cus_1 is stamped with
// flowOrg and has an address (search finds it unless unsearchable), cus_other is stamped with
// flowOther. Invoices live in invoices; a create or an add_lines with a known Idempotency-Key
// replays its first response, even for a deleted invoice, as Stripe does for 24 h. A finalized
// invoice totals its net plus taxPercent. fail (a path suffix) answers 500; every call is recorded
// as "METHOD path" and every Idempotency-Key in keys.
type deferredFake struct {
	mu           sync.Mutex
	invoices     map[string]*fakeInvoice
	order        []string
	replays      map[string]string
	keys         []string
	forms        map[string]map[string]string // the last form of each "METHOD path"
	calls        []string
	fail         string
	unsearchable bool
	taxPercent   int64
}

func newDeferredFake(t *testing.T) (*deferredFake, *stripe.Client) {
	f := &deferredFake{
		invoices: map[string]*fakeInvoice{}, replays: map[string]string{}, forms: map[string]map[string]string{},
		taxPercent: 21,
	}
	return f, stubStripe(t, f)
}

func (f *deferredFake) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, call := range f.calls {
		if !strings.HasPrefix(call, http.MethodGet) {
			out = append(out, call)
		}
	}
	return out
}

// alive are the invoices not deleted, oldest first.
func (f *deferredFake) alive() []*fakeInvoice {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeInvoice
	for _, id := range f.order {
		if !f.invoices[id].Deleted {
			out = append(out, f.invoices[id])
		}
	}
	return out
}

func (f *deferredFake) json(inv *fakeInvoice) string {
	total := inv.Net
	if inv.Status != "draft" {
		total = inv.Net * (100 + f.taxPercent) / 100
	}
	md, _ := json.Marshal(inv.Metadata)
	return fmt.Sprintf(`{"id":%q,"object":"invoice","number":"VOC-%s","status":%q,"customer":%q,"created":%d,`+
		`"total_excluding_tax":%d,"total":%d,"invoice_pdf":"https://pay.stripe.com/invoice/%s/pdf","metadata":%s}`,
		inv.ID, inv.ID, inv.Status, inv.Customer, inv.Created, inv.Net, total, inv.ID, md)
}

func (f *deferredFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	_ = r.ParseForm()
	form := map[string]string{}
	for k, v := range r.PostForm {
		form[k] = v[0]
	}
	f.forms[call] = form
	key := r.Header.Get("Idempotency-Key")
	if strings.HasPrefix(key, "topup-") { // stripe-go adds random keys of its own to other POSTs
		f.keys = append(f.keys, key)
	}
	missing := func() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
	}
	if f.fail != "" && strings.HasSuffix(r.URL.Path, f.fail) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"stripe is down"}}`))
		return
	}
	if replay, ok := f.replays[key]; ok && key != "" {
		_, _ = w.Write([]byte(replay))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/invoices/"), "/")
	inv := f.invoices[parts[0]]
	respond := func(body string) {
		if key != "" {
			f.replays[key] = body
		}
		_, _ = w.Write([]byte(body))
	}
	switch {
	case r.URL.Path == "/v1/customers/search":
		data := ""
		if !f.unsearchable {
			data = `{"id":"cus_1","object":"customer","address":{"country":"ES"},"metadata":{"address":"` + flowOrg.Hex() + `"}}`
		}
		_, _ = w.Write([]byte(`{"object":"search_result","has_more":false,"data":[` + data + `]}`))
	case r.URL.Path == "/v1/customers/cus_1":
		_, _ = w.Write([]byte(`{"id":"cus_1","object":"customer","address":{"country":"ES"},"metadata":{"address":"` +
			flowOrg.Hex() + `"}}`))
	case r.URL.Path == "/v1/customers/cus_other":
		_, _ = w.Write([]byte(`{"id":"cus_other","object":"customer","address":{"country":"ES"},"metadata":{"address":"` +
			flowOther.Hex() + `"}}`))
	case r.URL.Path == "/v1/invoices" && r.Method == http.MethodGet:
		var data []string
		for _, id := range slices.Backward(f.order) { // newest first
			if i := f.invoices[id]; !i.Deleted && i.Customer == r.URL.Query().Get("customer") {
				data = append(data, f.json(i))
			}
		}
		_, _ = w.Write([]byte(`{"object":"list","url":"/v1/invoices","has_more":false,"data":[` + strings.Join(data, ",") + `]}`))
	case r.URL.Path == "/v1/invoices":
		id := "in_" + strconv.Itoa(len(f.order)+1)
		metadata := map[string]string{}
		for k, v := range form {
			if name, ok := strings.CutPrefix(k, "metadata["); ok {
				metadata[strings.TrimSuffix(name, "]")] = v
			}
		}
		f.invoices[id] = &fakeInvoice{
			ID: id, Status: "draft", Customer: form["customer"], Metadata: metadata,
			Created: int64(1_000 + len(f.order)),
		}
		f.order = append(f.order, id)
		respond(f.json(f.invoices[id]))
	case inv == nil || inv.Deleted:
		missing()
	case r.Method == http.MethodGet:
		_, _ = w.Write([]byte(f.json(inv)))
	case r.Method == http.MethodDelete:
		inv.Deleted = true
		_, _ = w.Write([]byte(`{"id":"` + inv.ID + `","object":"invoice","deleted":true}`))
	case len(parts) > 1 && parts[1] == "add_lines":
		amount, _ := strconv.ParseInt(form["lines[0][price_data][unit_amount]"], 10, 64)
		inv.Net += amount
		inv.Lines = append(inv.Lines, form["lines[0][description]"])
		inv.Products = append(inv.Products, form["lines[0][price_data][product_data][name]"])
		respond(f.json(inv))
	case len(parts) > 1 && parts[1] == "finalize":
		inv.Status = "open"
		respond(f.json(inv))
	case len(parts) > 1 && parts[1] == "pay" && form["paid_out_of_band"] == "true":
		inv.Status = "paid"
		respond(f.json(inv))
	default:
		missing()
	}
}

// TestIssueDeferredInvoice (AC4): a deferred invoice is issued New → AddLines → Finalize → Pay
// (out of band) to the org's stamped customer, source deferred with the ledger key, its id stored
// right after New, one line dated by the day the money arrived, under per-attempt Idempotency-Keys.
func TestIssueDeferredInvoice(t *testing.T) {
	c := qt.New(t)
	f, client := newDeferredFake(t)
	key := stripe.ManualCreditKeyPrefix + "0123456789abcdef01234567"
	var recorded []string
	in := DeferredInvoice{
		Org: flowOrg, LedgerKey: key, AmountCents: 50_000, ReceivedCents: 60_500, Description: "transfer",
		ReceivedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Attempt: 1,
		OnCreated: func(id string) error { recorded = append(recorded, id); return nil },
	}

	result, err := IssueDeferredInvoice(context.Background(), client, in)
	c.Assert(err, qt.IsNil)
	c.Assert(result.Invoice.ID, qt.Equals, "in_1")
	c.Assert(result.Invoice.Status, qt.Equals, "paid")
	c.Assert(result.Invoice.TotalCents, qt.Equals, in.ReceivedCents)
	c.Assert(recorded, qt.DeepEquals, []string{"in_1"})
	c.Assert(f.writes(), qt.DeepEquals, []string{
		"POST /v1/invoices", "POST /v1/invoices/in_1/add_lines", "POST /v1/invoices/in_1/finalize", "POST /v1/invoices/in_1/pay",
	})
	c.Assert(f.keys, qt.DeepEquals, []string{"topup-deferred:" + key + ":1:cus_1", "topup-deferred-lines:in_1"})
	issued := f.invoices["in_1"]
	c.Assert(issued.Customer, qt.Equals, "cus_1")
	c.Assert(issued.Metadata[stripe.MetadataKeyWalletTopUpSource], qt.Equals, stripe.TopUpSourceDeferred)
	c.Assert(issued.Metadata[stripe.MetadataKeyWalletLedgerKey], qt.Equals, key)
	c.Assert(issued.Metadata[stripe.MetadataKeyWalletTopUpOrg], qt.Equals, flowOrg.String())
	c.Assert(issued.Lines, qt.DeepEquals, []string{"transfer (pago recibido el 2026-11-20)"})
	c.Assert(issued.Products, qt.DeepEquals, []string{"transfer"})
	c.Assert(f.forms["POST /v1/invoices"]["effective_at"], qt.Equals, "")
	c.Assert(f.forms["POST /v1/invoices/in_1/finalize"]["auto_advance"], qt.DeepEquals, "false")
	c.Assert(f.forms["POST /v1/invoices"]["automatic_tax[enabled]"], qt.DeepEquals, "true")

	// already paid: found by the record's id, or listed when the id was lost; nothing written
	for _, id := range []string{"in_1", ""} {
		f.calls = nil
		again := in
		again.InvoiceID, again.Attempt = id, 2
		result, err = IssueDeferredInvoice(context.Background(), client, again)
		c.Assert(err, qt.IsNil)
		c.Assert(result.Invoice.ID, qt.Equals, "in_1")
		c.Assert(f.writes(), qt.HasLen, 0)
	}
}

// TestIssueDeferredInvoiceResume (AC4): a retry resumes the invoice the failed attempt left and
// never issues a second invoice nor a second line: after a failed Finalize the draft is deleted
// and created again (a retry under the same attempt replays the deleted draft and fails, which is
// why every attempt has its own key); after a failed Pay it only pays; a draft whose id was never
// recorded is found by listing, and a deleted or void one is created again. A finalized total
// other than the gross received is never paid, on the attempt nor on a retry.
func TestIssueDeferredInvoiceResume(t *testing.T) {
	c := qt.New(t)
	f, client := newDeferredFake(t)
	key := stripe.ManualCreditKeyPrefix + "89abcdef0123456701234567"
	record := struct{ invoiceID string }{}
	in := DeferredInvoice{
		Org: flowOrg, LedgerKey: key, AmountCents: 50_000, ReceivedCents: 60_500, Description: "transfer",
		ReceivedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		OnCreated:  func(id string) error { record.invoiceID = id; return nil },
	}
	attempt := func() (*Result, error) {
		in.Attempt++
		in.InvoiceID = record.invoiceID
		f.mu.Lock()
		f.calls = nil
		f.mu.Unlock()
		return IssueDeferredInvoice(context.Background(), client, in)
	}
	stage := func(err error) (string, string) {
		var execErr *ExecuteError
		c.Assert(errors.As(err, &execErr), qt.IsTrue, qt.Commentf("%v", err))
		return execErr.Stage, execErr.InvoiceID
	}

	// finalize fails: the draft is recorded; the retry deletes it and issues a new one with one line
	f.fail = "/finalize"
	_, err := attempt()
	s, id := stage(err)
	c.Assert([]string{s, id, record.invoiceID}, qt.DeepEquals, []string{StageFinalize, "in_1", "in_1"})
	f.fail = ""
	_, err = attempt()
	c.Assert(err, qt.IsNil)
	c.Assert(f.writes(), qt.DeepEquals, []string{
		"DELETE /v1/invoices/in_1", "POST /v1/invoices", "POST /v1/invoices/in_2/add_lines",
		"POST /v1/invoices/in_2/finalize", "POST /v1/invoices/in_2/pay",
	})
	alive := f.alive()
	c.Assert(alive, qt.HasLen, 1)
	c.Assert(alive[0].ID, qt.Equals, "in_2")
	c.Assert(alive[0].Lines, qt.HasLen, 1)

	// a retry under the same attempt as a deleted draft gets that draft back from Stripe and fails at
	// AddLines; the next attempt has its own key and issues the invoice
	in.LedgerKey, record.invoiceID, in.Attempt = stripe.ManualCreditKeyPrefix+"aaaaaaaaaaaaaaaaaaaaaaaa", "", 0
	f.fail = "/add_lines"
	_, err = attempt()
	s, id = stage(err)
	c.Assert([]string{s, id}, qt.DeepEquals, []string{StageAddLines, "in_3"})
	f.fail = ""
	in.Attempt--
	_, err = attempt()
	s, id = stage(err)
	c.Assert([]string{s, id}, qt.DeepEquals, []string{StageAddLines, "in_3"})
	c.Assert(f.writes(), qt.DeepEquals, []string{
		"DELETE /v1/invoices/in_3", "POST /v1/invoices", "POST /v1/invoices/in_3/add_lines",
	})
	result, err := attempt()
	c.Assert(err, qt.IsNil)
	c.Assert(result.Invoice.ID, qt.Equals, "in_4")
	c.Assert(f.invoices["in_4"].Lines, qt.HasLen, 1)

	// pay fails: the retry only pays the open invoice
	in.LedgerKey, record.invoiceID = stripe.ManualCreditKeyPrefix+"bbbbbbbbbbbbbbbbbbbbbbbb", ""
	f.fail = "/pay"
	_, err = attempt()
	s, id = stage(err)
	c.Assert([]string{s, id}, qt.DeepEquals, []string{StagePay, "in_5"})
	f.fail = ""
	_, err = attempt()
	c.Assert(err, qt.IsNil)
	c.Assert(f.writes(), qt.DeepEquals, []string{"POST /v1/invoices/in_5/pay"})

	// the id is never recorded: the retry lists the customer's invoices, deletes the draft, issues anew
	in.LedgerKey, record.invoiceID = stripe.ManualCreditKeyPrefix+"cccccccccccccccccccccccc", ""
	in.OnCreated = func(string) error { return errors.New("mongo down") }
	_, err = attempt()
	s, id = stage(err)
	c.Assert([]string{s, id, record.invoiceID}, qt.DeepEquals, []string{StageRecord, "in_6", ""})
	c.Assert(f.writes(), qt.DeepEquals, []string{"POST /v1/invoices"})
	in.OnCreated = func(id string) error { record.invoiceID = id; return nil }
	_, err = attempt()
	c.Assert(err, qt.IsNil)
	c.Assert(f.writes()[0], qt.Equals, "DELETE /v1/invoices/in_6")
	c.Assert(record.invoiceID, qt.Equals, "in_7")

	// the operator voided it: created again
	f.invoices["in_7"].Status = "void"
	_, err = attempt()
	c.Assert(err, qt.IsNil)
	c.Assert(f.writes()[0], qt.Equals, "POST /v1/invoices")
	c.Assert(record.invoiceID, qt.Equals, "in_8")

	// the total is not the gross received (another tax treatment): left open, never paid
	in.LedgerKey, record.invoiceID = stripe.ManualCreditKeyPrefix+"dddddddddddddddddddddddd", ""
	f.taxPercent = 0
	for range 2 {
		_, err = attempt()
		s, id = stage(err)
		c.Assert([]string{s, id}, qt.DeepEquals, []string{StageTotal, "in_9"})
		c.Assert(err, qt.ErrorMatches, "invoice in_9 totals 50000 eur cents but 60500 were received.*")
		c.Assert(slices.Contains(f.writes(), "POST /v1/invoices/in_9/pay"), qt.IsFalse)
	}
	c.Assert(f.invoices["in_9"].Status, qt.Equals, "open")
}

// TestIssueDeferredInvoiceRefusals (AC4): no invoice without a stamped customer with an address,
// for a key that is not a manual credit's, or over an uncollectible invoice or an open one for
// another net amount; a just registered customer is found by its id while the search lags.
func TestIssueDeferredInvoiceRefusals(t *testing.T) {
	c := qt.New(t)
	f, client := newDeferredFake(t)
	in := DeferredInvoice{
		Org: flowOrg, LedgerKey: stripe.ManualCreditKeyPrefix + "eeeeeeeeeeeeeeeeeeeeeeee", AmountCents: 50_000,
		ReceivedCents: 60_500, Description: "transfer", ReceivedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		Attempt: 1, OnCreated: func(string) error { return nil },
	}
	issue := func(mutate func(d *DeferredInvoice)) error {
		d := in
		mutate(&d)
		_, err := IssueDeferredInvoice(context.Background(), client, d)
		return err
	}
	for name, mutate := range map[string]func(d *DeferredInvoice){
		"external key":  func(d *DeferredInvoice) { d.LedgerKey = stripe.ExternalCreditKeyPrefix + "eeeeeeeeeeeeeeeeeeeeeeee" },
		"short key":     func(d *DeferredInvoice) { d.LedgerKey = stripe.ManualCreditKeyPrefix + "e" },
		"received less": func(d *DeferredInvoice) { d.ReceivedCents = 49_999 },
		"no date":       func(d *DeferredInvoice) { d.ReceivedAt = time.Time{} },
		"no OnCreated":  func(d *DeferredInvoice) { d.OnCreated = nil },
	} {
		c.Assert(issue(mutate), qt.ErrorIs, ErrInvalid, qt.Commentf("%s", name))
	}
	c.Assert(f.calls, qt.HasLen, 0)

	f.unsearchable = true
	c.Assert(issue(func(*DeferredInvoice) {}), qt.ErrorIs, ErrNoCustomer)
	c.Assert(issue(func(d *DeferredInvoice) { d.CustomerID = "cus_other" }), qt.ErrorIs, ErrCustomerOtherOrg)
	c.Assert(issue(func(d *DeferredInvoice) { d.CustomerID = "cus_1" }), qt.IsNil)
	f.unsearchable = false

	f.invoices["in_1"].Status = "uncollectible"
	c.Assert(issue(func(d *DeferredInvoice) { d.Attempt = 2 }), qt.ErrorIs, ErrDuplicate)
	f.invoices["in_1"].Status, f.invoices["in_1"].Net = "open", 40_000
	c.Assert(issue(func(d *DeferredInvoice) { d.Attempt = 2 }), qt.ErrorIs, ErrDuplicate)
	c.Assert(f.writes(), qt.DeepEquals, []string{
		"POST /v1/invoices", "POST /v1/invoices/in_1/add_lines", "POST /v1/invoices/in_1/finalize", "POST /v1/invoices/in_1/pay",
	})
}
