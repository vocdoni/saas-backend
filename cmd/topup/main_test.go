package main

import (
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

// TestValidate: the flags alone decide the amount bounds (€1 to €100 000 net), that exactly one
// mode is given, and that the organization, description and database are named.
func TestValidate(t *testing.T) {
	c := qt.New(t)
	valid := options{
		mongoURL: "mongodb://localhost", org: common.Address{0x01}.String(), amountCents: 50_000,
		description: "wallet top-up", paidOutOfBand: true,
	}
	c.Assert(validate(&valid), qt.IsNil)
	for name, mutate := range map[string]func(o *options){
		"no database":       func(o *options) { o.mongoURL = "" },
		"not an address":    func(o *options) { o.org = "0x1234" },
		"below the minimum": func(o *options) { o.amountCents = int64(pricing.MinimumChargeCents) - 1 },
		"above the maximum": func(o *options) { o.amountCents = maxAmountCents + 1 },
		"no description":    func(o *options) { o.description = " " },
		"long description":  func(o *options) { o.description = strings.Repeat("é", maxDescriptionChars+1) },
		"both modes":        func(o *options) { o.send = true },
		"neither mode":      func(o *options) { o.paidOutOfBand = false },
	} {
		c.Run(name, func(c *qt.C) {
			opts := valid
			mutate(&opts)
			c.Assert(validate(&opts), qt.IsNotNil)
		})
	}
	// the description is trimmed in place, and 250 characters (not bytes) fit
	padded := valid
	padded.description = "  " + strings.Repeat("é", maxDescriptionChars) + "\n"
	c.Assert(validate(&padded), qt.IsNil)
	c.Assert(padded.description, qt.Equals, strings.Repeat("é", maxDescriptionChars))
	sent := valid
	sent.paidOutOfBand, sent.send = false, true
	c.Assert(validate(&sent), qt.IsNil)
}

// TestChecks: a managed organization has no wallet to top up, and a customer with no address has no tax location.
func TestChecks(t *testing.T) {
	c := qt.New(t)
	c.Assert(checkOrganization(&db.Organization{Address: common.Address{0x01}}), qt.IsNil)
	c.Assert(checkOrganization(&db.Organization{Address: common.Address{0x01}, ManagedBy: common.Address{0x02}}),
		qt.ErrorMatches, ".*is managed by.*")
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1"}), qt.ErrorMatches, "customer cus_1 has no address.*")
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1", Address: &stripeapi.Address{}}), qt.ErrorMatches,
		"customer cus_1 has no address.*")
	c.Assert(checkCustomer(&stripeapi.Customer{ID: "cus_1", Address: &stripeapi.Address{Country: "ES"}}), qt.IsNil)
}

// TestDuplicateInvoices: an invoice is the same top-up issued before when it is a manual top-up of
// the organization, not void, and its metadata names the same net amount and description,
// whatever its status otherwise and whatever its lines (a draft a run that died halfway left
// has none). How far back invoices are looked at is the list's filter, not checked here. The
// refusal tells what to do with each by its status (describeDuplicate).
func TestDuplicateInvoices(t *testing.T) {
	c := qt.New(t)
	org, other := common.Address{0x01}, common.Address{0x02}
	invoice := func(id string, status stripeapi.InvoiceStatus, o common.Address, source string, amount int64,
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
	manual := stripe.TopUpSourceManual
	invoices := []*stripeapi.Invoice{
		invoice("in_draft", stripeapi.InvoiceStatusDraft, org, manual, 50_000, "wallet top-up"),
		invoice("in_open", stripeapi.InvoiceStatusOpen, org, manual, 50_000, "wallet top-up"),
		invoice("in_paid", stripeapi.InvoiceStatusPaid, org, manual, 50_000, "wallet top-up"),
		invoice("in_uncollectible", stripeapi.InvoiceStatusUncollectible, org, manual, 50_000, "wallet top-up"),
		invoice("in_void", stripeapi.InvoiceStatusVoid, org, manual, 50_000, "wallet top-up"),
		invoice("in_amount", stripeapi.InvoiceStatusPaid, org, manual, 60_000, "wallet top-up"),
		invoice("in_description", stripeapi.InvoiceStatusPaid, org, manual, 50_000, "wallet top-up (march)"),
		invoice("in_other", stripeapi.InvoiceStatusPaid, other, manual, 50_000, "wallet top-up"),
		invoice("in_checkout", stripeapi.InvoiceStatusPaid, org, stripe.TopUpSourceCheckout, 50_000, "wallet top-up"),
		{ID: "in_subscription", Status: stripeapi.InvoiceStatusPaid},
	}
	var ids []string
	for _, duplicate := range duplicateInvoices(invoices, org, &options{amountCents: 50_000, description: "wallet top-up"}) {
		ids = append(ids, duplicate.ID)
	}
	c.Assert(ids, qt.DeepEquals, []string{"in_draft", "in_open", "in_paid", "in_uncollectible"})
	c.Assert(duplicateInvoices(invoices, org, &options{amountCents: 70_000, description: "wallet top-up"}), qt.HasLen, 0)

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
	params := buildInvoiceParams(org, "cus_1", &options{amountCents: 50_000, description: "wallet top-up (bank transfer)"})
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
