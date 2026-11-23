package topup

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
)

var (
	flowOrg     = common.HexToAddress("0x000000000000000000000000000000000000bEEF")
	flowManaged = common.HexToAddress("0x000000000000000000000000000000000000cAFE")
	flowOther   = common.HexToAddress("0x000000000000000000000000000000000000dEaD")
)

// fakeStripe is a Stripe account holding one organization's customer and its invoices. It records
// every call as "METHOD path", answers fail (a path suffix) with a 500, and stamps customers.
type fakeStripe struct {
	mu       sync.Mutex
	calls    []string
	stamped  map[string]string
	listed   string // the data of GET /v1/invoices (every customer, the last 7 days)
	open     string // the data of GET /v1/invoices?customer=…&status=open
	fail     string
	notFound bool // the customer search finds nobody
}

func (f *fakeStripe) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
}

func (f *fakeStripe) writes() []string {
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

func flowInvoice(id, status string) string {
	return `{"id":"` + id + `","object":"invoice","number":"VOC-0042","status":"` + status + `",` +
		`"customer":"cus_1","created":1762732800,"total_excluding_tax":50000,"total":60500,` +
		`"hosted_invoice_url":"https://invoice.stripe.com/i/` + id + `",` +
		`"invoice_pdf":"https://pay.stripe.com/invoice/acct_1/` + id + `/pdf"}`
}

// manualInvoiceJSON is a manual top-up invoice of org for amount and description, as Stripe lists it.
func manualInvoiceJSON(id, status string, org common.Address, amount, description string) string {
	return `{"id":"` + id + `","object":"invoice","status":"` + status + `","customer":"cus_1",` +
		`"metadata":{"` + stripe.MetadataKeyWalletTopUpOrg + `":"` + org.Hex() + `","` +
		stripe.MetadataKeyWalletTopUpSource + `":"` + stripe.TopUpSourceManual + `","` +
		metadataKeyAmountCents + `":"` + amount + `","` + metadataKeyDescription + `":"` + description + `"}}`
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	if f.fail != "" && strings.HasSuffix(r.URL.Path, f.fail) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"stripe down"}}`))
		return
	}
	list := func(data string) {
		_, _ = w.Write([]byte(`{"object":"list","url":"` + r.URL.Path + `","has_more":false,"data":[` + data + `]}`))
	}
	switch {
	case r.URL.Path == "/v1/customers/search":
		data := ""
		if !f.notFound {
			data = `{"id":"cus_1","object":"customer","name":"Golden SL","email":"billing@golden.test",` +
				`"address":{"line1":"Carrer 1","city":"Barcelona","country":"ES"},"metadata":{"address":"` + flowOrg.Hex() + `"}}`
		}
		_, _ = w.Write([]byte(`{"object":"search_result","has_more":false,"data":[` + data + `]}`))
	case r.URL.Path == "/v1/customers/cus_new" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		f.mu.Lock()
		f.stamped["cus_new"] = r.PostForm.Get("metadata[address]")
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"cus_new","object":"customer"}`))
	case r.URL.Path == "/v1/customers/cus_new":
		_, _ = w.Write([]byte(`{"id":"cus_new","object":"customer","email":"x@y.test","address":{"country":"ES"},"metadata":{}}`))
	case r.URL.Path == "/v1/checkout/sessions":
		list(`{"id":"cs_1","object":"checkout.session","metadata":{"` + stripe.MetadataKeyWalletTopUpOrg + `":"` +
			flowOrg.Hex() + `"}}`)
	case r.URL.Path == "/v1/checkout/sessions/cs_1":
		_, _ = w.Write([]byte(`{"id":"cs_1","object":"checkout.session","invoice":"in_1"}`))
	case r.URL.Path == "/v1/checkout/sessions/cs_free":
		_, _ = w.Write([]byte(`{"id":"cs_free","object":"checkout.session","invoice":null}`))
	case r.URL.Path == "/v1/invoices" && r.Method == http.MethodGet && r.URL.Query().Get("customer") != "":
		list(f.open)
	case r.URL.Path == "/v1/invoices" && r.Method == http.MethodGet:
		list(f.listed)
	case r.URL.Path == "/v1/invoices", strings.HasSuffix(r.URL.Path, "/add_lines"):
		_, _ = w.Write([]byte(flowInvoice("in_1", "draft")))
	case strings.HasSuffix(r.URL.Path, "/finalize"), strings.HasSuffix(r.URL.Path, "/send"):
		_, _ = w.Write([]byte(flowInvoice("in_1", "open")))
	case strings.HasSuffix(r.URL.Path, "/pay"):
		_, _ = w.Write([]byte(flowInvoice("in_1", "paid")))
	case r.URL.Path == "/v1/invoices/in_1":
		_, _ = w.Write([]byte(flowInvoice("in_1", "paid")))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
	}
}

func newFlow(t *testing.T) (*fakeStripe, *stripe.Client, fakeStore) {
	f := &fakeStripe{stamped: map[string]string{}}
	client := stubStripe(t, f)
	ms := fakeStore{orgs: map[common.Address]*db.Organization{
		flowOrg:     {Address: flowOrg},
		flowManaged: {Address: flowManaged, ManagedBy: flowOrg},
	}, users: map[string]*db.User{
		"x@y.test": {Email: "x@y.test", Organizations: []db.OrganizationUser{{Address: flowOrg, Role: db.AdminRole}}},
	}}
	return f, client, ms
}

// TestPrepare: a plan names the stamped customer, the trimmed request and the duplicates found,
// and Prepare only reads Stripe (an unstamped customer that passed the evidence check is reported
// as Stamp, not stamped). Each refusal is typed; a duplicate refusal carries the duplicates and
// AllowDuplicate issues the plan anyway; a Stripe failure is untyped.
func TestPrepare(t *testing.T) {
	c := qt.New(t)
	f, client, ms := newFlow(t)
	ctx := context.Background()
	req := Request{Org: flowOrg, AmountCents: 50_000, Description: " wallet top-up ", Mode: Send}

	plan, err := Prepare(ctx, ms, client, req)
	c.Assert(err, qt.IsNil)
	c.Assert(plan, qt.DeepEquals, &Plan{
		Org: flowOrg, AmountCents: 50_000, Description: "wallet top-up", Mode: Send, Duplicates: []Invoice{},
		Customer: Customer{
			ID: "cus_1", Name: "Golden SL", Email: "billing@golden.test", Address: "Carrer 1, Barcelona, ES", Country: "ES",
		},
	})

	unstamped := req
	unstamped.CustomerID = "cus_new"
	f.notFound = true
	plan, err = Prepare(ctx, ms, client, unstamped)
	c.Assert(err, qt.IsNil)
	c.Assert(plan.Stamp, qt.IsTrue)
	c.Assert(plan.Customer.ID, qt.Equals, "cus_new")
	_, err = Prepare(ctx, ms, client, req)
	c.Assert(err, qt.ErrorIs, ErrNoCustomer)
	f.notFound = false

	f.listed = manualInvoiceJSON("in_dup", "open", flowOrg, "50000", "wallet top-up") + "," +
		manualInvoiceJSON("in_void", "void", flowOrg, "50000", "wallet top-up")
	_, err = Prepare(ctx, ms, client, req)
	c.Assert(err, qt.ErrorIs, ErrDuplicate)
	var duplicate *DuplicateError
	c.Assert(errors.As(err, &duplicate), qt.IsTrue)
	c.Assert(duplicate.Plan.Duplicates, qt.HasLen, 1)
	c.Assert(duplicate.Plan.Duplicates[0].ID, qt.Equals, "in_dup")
	c.Assert(err, qt.ErrorMatches, `an invoice for the same top-up .* in_dup \(open, customer cus_1\): .*`)
	allowed := req
	allowed.AllowDuplicate = true
	plan, err = Prepare(ctx, ms, client, allowed)
	c.Assert(err, qt.IsNil)
	c.Assert(plan.Duplicates, qt.HasLen, 1)

	_, err = Prepare(ctx, ms, client, Request{Org: flowOrg, AmountCents: 1, Description: "x", Mode: Send})
	c.Assert(err, qt.ErrorIs, ErrInvalid)
	managed := req
	managed.Org = flowManaged
	_, err = Prepare(ctx, ms, client, managed)
	c.Assert(err, qt.ErrorIs, ErrManagedOrg)
	unknown := req
	unknown.Org = flowOther
	_, err = Prepare(ctx, ms, client, unknown)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	f.fail = "/v1/invoices"
	_, err = Prepare(ctx, ms, client, req)
	c.Assert(err, qt.ErrorMatches, "could not list the invoices of the last 7 days.*")
	typedErrors := []error{ErrInvalid, ErrManagedOrg, ErrNoCustomer, ErrCustomerOtherOrg, ErrCustomerNoAddress, ErrDuplicate}
	for _, typed := range typedErrors {
		c.Assert(errors.Is(err, typed), qt.IsFalse)
	}
	c.Assert(f.writes(), qt.HasLen, 0)
	c.Assert(f.stamped, qt.HasLen, 0)
}

// TestExecute: a plan is issued New → AddLines → Finalize → Pay (paid out of band) or Send,
// stamping first when the plan says so, and the result is the invoice as Stripe left it. A failure
// names its stage and, from AddLines onwards, the invoice left behind.
func TestExecute(t *testing.T) {
	c := qt.New(t)
	f, client, _ := newFlow(t)
	ctx := context.Background()
	plan := &Plan{Org: flowOrg, Customer: Customer{ID: "cus_1"}, AmountCents: 50_000, Description: "wallet top-up"}
	issue := []string{"POST /v1/invoices", "POST /v1/invoices/in_1/add_lines", "POST /v1/invoices/in_1/finalize"}

	for mode, last := range map[Mode]string{PaidOutOfBand: "POST /v1/invoices/in_1/pay", Send: "POST /v1/invoices/in_1/send"} {
		f.calls = nil
		p := *plan
		p.Mode = mode
		result, err := Execute(ctx, client, &p)
		c.Assert(err, qt.IsNil)
		c.Assert(f.writes(), qt.DeepEquals, append(append([]string{}, issue...), last))
		c.Assert(result.Invoice.ID, qt.Equals, "in_1")
		c.Assert(result.Invoice.Number, qt.Equals, "VOC-0042")
		c.Assert(result.Invoice.AmountCents, qt.Equals, plan.AmountCents)
		c.Assert(result.Invoice.TotalCents, qt.Equals, plan.AmountCents+10_500)
		c.Assert(result.Invoice.HostedURL, qt.Equals, "https://invoice.stripe.com/i/in_1")
	}
	c.Assert(f.stamped, qt.HasLen, 0)

	f.calls = nil
	stamp := *plan
	stamp.Mode, stamp.Stamp, stamp.Customer.ID = Send, true, "cus_new"
	_, err := Execute(ctx, client, &stamp)
	c.Assert(err, qt.IsNil)
	c.Assert(f.writes()[0], qt.Equals, "POST /v1/customers/cus_new")
	c.Assert(f.stamped, qt.DeepEquals, map[string]string{"cus_new": flowOrg.String()})

	for _, tc := range []struct {
		fail, stage, invoiceID string
		mode                   Mode
		stamp                  bool
	}{
		{"/v1/customers/cus_new", StageStamp, "", Send, true},
		{"/v1/invoices", StageNew, "", Send, false},
		{"/add_lines", StageAddLines, "in_1", Send, false},
		{"/finalize", StageFinalize, "in_1", Send, false},
		{"/pay", StagePay, "in_1", PaidOutOfBand, false},
		{"/send", StageSend, "in_1", Send, false},
	} {
		f.fail = tc.fail
		p := stamp
		p.Mode, p.Stamp = tc.mode, tc.stamp
		_, err := Execute(ctx, client, &p)
		var execErr *ExecuteError
		c.Assert(errors.As(err, &execErr), qt.IsTrue, qt.Commentf("fail %s", tc.fail))
		c.Assert(execErr.Stage, qt.Equals, tc.stage)
		c.Assert(execErr.InvoiceID, qt.Equals, tc.invoiceID)
	}
}

// TestOpenManualInvoices: the open invoices of the organization's stamped customer that are its
// manual top-ups; none, without listing, when no customer is stamped; a failed search or list is
// an error.
func TestOpenManualInvoices(t *testing.T) {
	c := qt.New(t)
	f, client, _ := newFlow(t)
	f.open = manualInvoiceJSON("in_mine", "open", flowOrg, "50000", "wallet top-up") + "," +
		manualInvoiceJSON("in_theirs", "open", flowOther, "50000", "wallet top-up") + "," +
		`{"id":"in_subscription","object":"invoice","status":"open"}`
	invoices, err := OpenManualInvoices(client, flowOrg)
	c.Assert(err, qt.IsNil)
	c.Assert(invoices, qt.HasLen, 1)
	c.Assert(invoices[0].ID, qt.Equals, "in_mine")
	c.Assert(invoices[0].CustomerID, qt.Equals, "cus_1")

	f.notFound, f.calls = true, nil
	invoices, err = OpenManualInvoices(client, flowOrg)
	c.Assert(err, qt.IsNil)
	c.Assert(invoices, qt.DeepEquals, []Invoice{})
	c.Assert(f.calls, qt.DeepEquals, []string{"GET /v1/customers/search"})

	f.notFound, f.fail = false, "/v1/invoices"
	_, err = OpenManualInvoices(client, flowOrg)
	c.Assert(err, qt.ErrorMatches, "could not list the open invoices of customer cus_1.*")
	f.fail = "/search"
	_, err = OpenManualInvoices(client, flowOrg)
	c.Assert(err, qt.IsNotNil)
	c.Assert(f.writes(), qt.HasLen, 0)
}

// TestInvoiceOfLedgerKey: a manual top-up's key names its invoice and a checkout top-up's key
// resolves it through the session, with its PDF URL; a session without an invoice or unknown to
// Stripe is stripe.ErrNoInvoice; a session Stripe fails to serve is a Stripe error.
func TestInvoiceOfLedgerKey(t *testing.T) {
	c := qt.New(t)
	f, client, _ := newFlow(t)
	for _, key := range []string{"cs_1", stripe.InvoiceTopUpKey("in_1")} {
		invoice, err := InvoiceOfLedgerKey(client, key)
		c.Assert(err, qt.IsNil, qt.Commentf("key %s", key))
		c.Assert(invoice.ID, qt.Equals, "in_1")
		c.Assert(invoice.Number, qt.Equals, "VOC-0042")
		c.Assert(invoice.PDFURL, qt.Equals, "https://pay.stripe.com/invoice/acct_1/in_1/pdf")
	}
	for _, key := range []string{"cs_free", "cs_unknown"} {
		_, err := InvoiceOfLedgerKey(client, key)
		c.Assert(err, qt.ErrorIs, stripe.ErrNoInvoice, qt.Commentf("key %s", key))
	}
	f.fail = "/cs_broken"
	_, err := InvoiceOfLedgerKey(client, "cs_broken")
	c.Assert(err, qt.ErrorMatches, ".*failed to get checkout session.*")
	c.Assert(f.writes(), qt.HasLen, 0)
}
