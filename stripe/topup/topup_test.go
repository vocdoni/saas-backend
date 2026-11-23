package topup

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// fakeStore is a Store of organizations by address and users by (normalized) email;
// "boom@org.test" fails.
type fakeStore struct {
	orgs  map[common.Address]*db.Organization
	users map[string]*db.User
}

func (f fakeStore) Organization(address common.Address) (*db.Organization, error) {
	if o, ok := f.orgs[address]; ok {
		return o, nil
	}
	return nil, db.ErrNotFound
}

func (f fakeStore) UserByEmail(email string) (*db.User, error) {
	if email == "boom@org.test" {
		return nil, fmt.Errorf("mongo down")
	}
	if u, ok := f.users[email]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

// stubStripe points the Stripe client at handler for one test, with no network retries.
func stubStripe(t *testing.T, handler http.Handler) *stripe.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	return stripe.NewClient(&stripe.Config{APIKey: "sk_test_stub"})
}

// TestValidate: the request alone decides the amount bounds (€1 to €100 000 net), the
// description (trimmed, 1 to 250 characters, not bytes), that the mode is one of the two and that
// the organization is named; every refusal is ErrInvalid.
func TestValidate(t *testing.T) {
	c := qt.New(t)
	valid := Request{Org: common.Address{0x01}, AmountCents: 50_000, Description: "wallet top-up", Mode: PaidOutOfBand}
	got, err := Validate(valid)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, valid)
	for name, mutate := range map[string]func(r *Request){
		"no organization":   func(r *Request) { r.Org = common.Address{} },
		"below the minimum": func(r *Request) { r.AmountCents = pricing.MinimumChargeCents - 1 },
		"above the maximum": func(r *Request) { r.AmountCents = MaxAmountCents + 1 },
		"no description":    func(r *Request) { r.Description = " " },
		"long description":  func(r *Request) { r.Description = strings.Repeat("é", MaxDescriptionChars+1) },
		"no mode":           func(r *Request) { r.Mode = "" },
		"unknown mode":      func(r *Request) { r.Mode = "charge" },
	} {
		c.Run(name, func(c *qt.C) {
			r := valid
			mutate(&r)
			_, err := Validate(r)
			c.Assert(err, qt.ErrorIs, ErrInvalid)
		})
	}
	padded := valid
	padded.Description, padded.Mode = "  "+strings.Repeat("é", MaxDescriptionChars)+"\n", Send
	got, err = Validate(padded)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Description, qt.Equals, strings.Repeat("é", MaxDescriptionChars))
	c.Assert(padded.Description, qt.Not(qt.Equals), got.Description) // the caller's request is left as it was
	minimum, maximum := valid, valid
	minimum.AmountCents, maximum.AmountCents = pricing.MinimumChargeCents, MaxAmountCents
	_, err = Validate(minimum)
	c.Assert(err, qt.IsNil)
	_, err = Validate(maximum)
	c.Assert(err, qt.IsNil)
}

// TestChecks: a managed organization has no wallet to top up, and a customer with no address has
// no tax location.
func TestChecks(t *testing.T) {
	c := qt.New(t)
	c.Assert(checkOrganization(&db.Organization{Address: common.Address{0x01}}), qt.IsNil)
	c.Assert(checkOrganization(&db.Organization{Address: common.Address{0x01}, ManagedBy: common.Address{0x02}}),
		qt.ErrorIs, ErrManagedOrg)
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1"}), qt.ErrorMatches, ".*customer cus_1 has no address.*")
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1", Address: &stripeapi.Address{}}), qt.ErrorIs,
		ErrCustomerNoAddress)
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1", Address: &stripeapi.Address{Country: "ES"}}), qt.IsNil)
}

// manualInvoice is a manual top-up invoice of o in the status, as duplicateInvoices reads it.
func manualInvoice(id string, status stripeapi.InvoiceStatus, o common.Address, source string, amount int64,
	description string,
) *stripeapi.Invoice {
	return &stripeapi.Invoice{
		ID: id, Status: status,
		Metadata: map[string]string{
			stripe.MetadataKeyWalletTopUpOrg: o.String(), stripe.MetadataKeyWalletTopUpSource: source,
			metadataKeyAmountCents: strconv.FormatInt(amount, 10), metadataKeyDescription: description,
		},
	}
}

// TestDuplicateInvoices: an invoice is the same top-up issued before when it is a manual top-up of
// the organization, not void, and its metadata names the same net amount and description,
// whatever its status otherwise and whatever its lines (a draft a run that died halfway left
// has none). How far back invoices are looked at is the list's filter, not checked here. The
// refusal tells what to do with each by its status (describeDuplicate).
func TestDuplicateInvoices(t *testing.T) {
	c := qt.New(t)
	org, other := common.Address{0x01}, common.Address{0x02}
	manual := stripe.TopUpSourceManual
	invoices := []*stripeapi.Invoice{
		manualInvoice("in_draft", stripeapi.InvoiceStatusDraft, org, manual, 50_000, "wallet top-up"),
		manualInvoice("in_open", stripeapi.InvoiceStatusOpen, org, manual, 50_000, "wallet top-up"),
		manualInvoice("in_paid", stripeapi.InvoiceStatusPaid, org, manual, 50_000, "wallet top-up"),
		manualInvoice("in_uncollectible", stripeapi.InvoiceStatusUncollectible, org, manual, 50_000, "wallet top-up"),
		manualInvoice("in_void", stripeapi.InvoiceStatusVoid, org, manual, 50_000, "wallet top-up"),
		manualInvoice("in_amount", stripeapi.InvoiceStatusPaid, org, manual, 60_000, "wallet top-up"),
		manualInvoice("in_description", stripeapi.InvoiceStatusPaid, org, manual, 50_000, "wallet top-up (march)"),
		manualInvoice("in_other", stripeapi.InvoiceStatusPaid, other, manual, 50_000, "wallet top-up"),
		manualInvoice("in_checkout", stripeapi.InvoiceStatusPaid, org, stripe.TopUpSourceCheckout, 50_000, "wallet top-up"),
		{ID: "in_subscription", Status: stripeapi.InvoiceStatusPaid},
	}
	var ids []string
	for _, duplicate := range duplicateInvoices(invoices, Request{Org: org, AmountCents: 50_000, Description: "wallet top-up"}) {
		ids = append(ids, duplicate.ID)
	}
	c.Assert(ids, qt.DeepEquals, []string{"in_draft", "in_open", "in_paid", "in_uncollectible"})
	c.Assert(duplicateInvoices(invoices, Request{Org: org, AmountCents: 70_000, Description: "wallet top-up"}), qt.HasLen, 0)

	// what to do about a duplicate depends on how far the run that issued it got
	invoices[0].Customer = &stripeapi.Customer{ID: "cus_1"}
	c.Assert(describeDuplicate(invoices[0]), qt.Equals,
		"in_draft (draft, customer cus_1): delete it in the dashboard and re-run")
	c.Assert(describeDuplicate(invoices[1]), qt.Equals, "in_open (open, customer unknown): "+
		"if that run finished, nothing to do; otherwise mark it paid out of band or send it from the "+
		"dashboard (or void it and re-run)")
	for _, issued := range invoices[2:4] {
		c.Assert(describeDuplicate(issued), qt.Matches, `in_\w+ \(\w+, customer unknown\): already issued, nothing to do`)
	}
}

// TestBuildInvoiceParams pins the wire shape of a manual top-up invoice: sent and never charged
// by Stripe on its own, due in 30 days, EUR with automatic tax, marked manual for the webhook,
// and one tax-exclusive line for the net amount.
func TestBuildInvoiceParams(t *testing.T) {
	c := qt.New(t)
	org := common.HexToAddress("0x000000000000000000000000000000000000bEEF")
	params := buildInvoiceParams(org, "cus_1", 50_000, "wallet top-up (bank transfer)")
	c.Assert(*params.Customer, qt.Equals, "cus_1")
	c.Assert(*params.Currency, qt.Equals, "eur")
	c.Assert(*params.CollectionMethod, qt.Equals, "send_invoice")
	c.Assert(*params.DaysUntilDue, qt.Equals, int64(30))
	c.Assert(*params.AutoAdvance, qt.IsFalse)
	c.Assert(*params.AutomaticTax.Enabled, qt.IsTrue)
	c.Assert(params.Metadata, qt.DeepEquals, map[string]string{
		stripe.MetadataKeyWalletTopUpOrg:    org.String(),
		stripe.MetadataKeyWalletTopUpSource: stripe.TopUpSourceManual,
		metadataKeyAmountCents:              "50000",
		metadataKeyDescription:              "wallet top-up (bank transfer)",
	})

	lines := buildLineParams(50_000, "wallet top-up (bank transfer)")
	c.Assert(lines.Lines, qt.HasLen, 1)
	line := lines.Lines[0]
	c.Assert(*line.Quantity, qt.Equals, int64(1))
	c.Assert(*line.Description, qt.Equals, "wallet top-up (bank transfer)")
	c.Assert(*line.PriceData.Currency, qt.Equals, "eur")
	c.Assert(*line.PriceData.UnitAmount, qt.Equals, int64(50_000))
	c.Assert(*line.PriceData.TaxBehavior, qt.Equals, "exclusive")
	c.Assert(*line.PriceData.ProductData.Name, qt.Equals, "wallet top-up (bank transfer)")
}
