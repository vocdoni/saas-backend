// Package topup issues manual wallet top-ups as Stripe invoices: credit sold outside the embedded
// checkout (a bank transfer, a negotiated amount). It is what cmd/topup and the staff dashboard
// both run, so their checks cannot drift.
//
// The invoice is billed to the organization's Stripe customer with VAT by Stripe Tax, and the
// organization's wallet is credited once Stripe reports it paid: the invoice.paid webhook credits
// it exactly once, keyed by the invoice. Nothing here writes the wallet.
//
// The customer is the one GetCustomerByAddress finds for the organization. When none is found
// (customer search is eventually consistent, and a customer a checkout created may not be
// stamped yet), Request.CustomerID names the one to bill: the customer the organization's
// checkouts created, or one the operator creates in the Stripe Dashboard with the organization's
// billing address (and tax id) first. It is stamped with the organization address so later
// lookups find it, but only with evidence that it is the organization's and not another
// tenant's: a checkout session of the customer for this organization (wallet_topup_org), or, for
// a customer with no checkout session at all, an email that is an Admin of this organization and
// belongs to no other one. A customer is never created, and one without an address is refused:
// it has no tax location, and automatic tax would fail at finalization.
//
// The invoice is created with collection_method send_invoice, due in 30 days, auto_advance off,
// so Stripe never charges a saved payment method on its own. Exactly one Mode is given:
// PaidOutOfBand (the money already arrived) or Send (the customer pays from Stripe's hosted
// invoice page). Keep the Dashboard setting "email finalized invoices" off, or Send emails twice.
//
// Issuing the same top-up twice would bill it twice, so Prepare refuses when the organization
// already has a manual top-up invoice, billed to any customer, created in the last 7 days and
// not void, for the same net amount and description (ErrDuplicate), unless AllowDuplicate.
//
// Prepare checks everything and writes nothing, in Stripe or in the database; Execute performs
// the writes of a Plan Prepare returned: the customer stamp, then the invoice. The Stripe
// credential is the process-wide key stripe.NewClient sets: one key per process.
package topup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripeinvoice "github.com/stripe/stripe-go/v87/invoice"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// Mode is how the invoice is settled once finalized.
type Mode string

const (
	// PaidOutOfBand marks the invoice paid out of band: the money already arrived.
	PaidOutOfBand Mode = "paid_out_of_band"
	// Send emails the invoice once; the customer pays from Stripe's hosted page.
	Send Mode = "send"
)

const (
	// MaxAmountCents is the largest top-up, the bound of POST /organizations/{orgAddress}/wallet/topup
	// (api.walletTopUpMaxCents); the minimum is pricing.MinimumChargeCents, like every charge.
	MaxAmountCents pricing.Cents = 100_000_00 // €100 000
	// MaxDescriptionChars is the longest description: it names the line's product and is kept in
	// the invoice metadata, and 250 characters stays within Stripe's limits for both.
	MaxDescriptionChars = 250
	// DaysUntilDue is how long a sent invoice stays payable.
	DaysUntilDue = 30
	// DuplicateWindow is how far back an invoice for the same top-up refuses a new one: long
	// enough to catch a retry, short enough that a recurring top-up is issued.
	DuplicateWindow = 7 * 24 * time.Hour
)

// Invoice metadata keys that name the top-up, so a later run recognizes it (duplicateInvoices)
// whatever lines the invoice has.
const (
	metadataKeyAmountCents = "topup_amount_cents"
	metadataKeyDescription = "topup_description"
)

// Typed refusals; every error Validate and Prepare return for them satisfies errors.Is.
var (
	ErrInvalid           = fmt.Errorf("invalid top-up")
	ErrManagedOrg        = fmt.Errorf("managed organization has no wallet")
	ErrNoCustomer        = fmt.Errorf("no stripe customer")
	ErrCustomerOtherOrg  = fmt.Errorf("stripe customer not tied to the organization")
	ErrCustomerNoAddress = fmt.Errorf("stripe customer has no address")
	ErrDuplicate         = fmt.Errorf("duplicate top-up")
)

// Store is the database reads Prepare needs.
type Store interface {
	Organization(address common.Address) (*db.Organization, error)
	UserByEmail(email string) (*db.User, error)
}

// Request is one manual top-up: what cmd/topup's flags carry.
type Request struct {
	Org            common.Address
	AmountCents    pricing.Cents // net, VAT excluded
	Description    string
	CustomerID     string // optional cus_… to bill when none is stamped with the organization
	Mode           Mode
	AllowDuplicate bool
}

// Customer is the Stripe customer a top-up is billed to, as the operator reviews it.
type Customer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Address string `json:"address"` // one line
	Country string `json:"country"`
}

// Invoice is a Stripe invoice of a top-up. PDFURL is a capability URL: it is never serialized.
type Invoice struct {
	ID          string        `json:"id"`
	Number      string        `json:"number"`
	Status      string        `json:"status"`
	CustomerID  string        `json:"customerId"`
	AmountCents pricing.Cents `json:"amountCents"` // net (total_excluding_tax)
	TotalCents  pricing.Cents `json:"totalCents"`  // with tax
	Created     time.Time     `json:"created"`
	HostedURL   string        `json:"hostedUrl"`
	PDFURL      string        `json:"-"`
}

// Plan is a checked top-up: everything verified, nothing created. Stamp reports that Execute
// stamps the customer with metadata.address (an unstamped CustomerID that passed the evidence
// check). Duplicates are the non-void manual invoices of the last 7 days with the same
// organization, amount and description.
type Plan struct {
	Org         common.Address `json:"org"`
	Customer    Customer       `json:"customer"`
	AmountCents pricing.Cents  `json:"amountCents"`
	Description string         `json:"description"`
	Mode        Mode           `json:"mode"`
	Stamp       bool           `json:"stamp"`
	Duplicates  []Invoice      `json:"duplicates"`
}

// Result is the invoice Execute issued.
type Result struct {
	Invoice Invoice `json:"invoice"`
}

// Validate checks the request alone: everything that needs no database or Stripe. It returns the
// request with the description trimmed, so the invoice line and the duplicate check use the same
// text. Every refusal wraps ErrInvalid.
func Validate(r Request) (Request, error) {
	if r.Org == (common.Address{}) {
		return r, fmt.Errorf("%w: the organization address is required", ErrInvalid)
	}
	if r.AmountCents < pricing.MinimumChargeCents || r.AmountCents > MaxAmountCents {
		return r, fmt.Errorf("%w: the amount must be between %d and %d eur cents", ErrInvalid,
			pricing.MinimumChargeCents, MaxAmountCents)
	}
	r.Description = strings.TrimSpace(r.Description)
	if r.Description == "" {
		return r, fmt.Errorf("%w: the description is required", ErrInvalid)
	}
	if n := utf8.RuneCountInString(r.Description); n > MaxDescriptionChars {
		return r, fmt.Errorf("%w: the description is %d characters long, at most %d fit (stripe limits)",
			ErrInvalid, n, MaxDescriptionChars)
	}
	if r.Mode != PaidOutOfBand && r.Mode != Send {
		return r, fmt.Errorf("%w: the mode must be %q or %q", ErrInvalid, PaidOutOfBand, Send)
	}
	return r, nil
}

// Prepare checks a top-up end to end and returns what Execute would issue: the request is
// valid, the organization has a wallet of its own, its customer resolves and has an address, and
// no duplicate exists unless AllowDuplicate (a *DuplicateError wrapping ErrDuplicate, with the
// duplicates attached). It writes nothing, in Stripe or in the database.
//
// It lists every invoice of the account created in the last 7 days, since Stripe cannot filter
// by metadata; at a few hundred invoices a week that is a handful of pages.
// ponytail: narrow it to the resolved customer plus a search by wallet_topup_org if it ever matters.
func Prepare(ctx context.Context, ms Store, c *stripe.Client, r Request) (*Plan, error) {
	r, err := Validate(r)
	if err != nil {
		return nil, err
	}
	org, err := ms.Organization(r.Org)
	if err != nil {
		return nil, fmt.Errorf("could not find organization %s: %w", r.Org, err)
	}
	if err := checkOrganization(org); err != nil {
		return nil, err
	}
	customer, stamp, err := resolveCustomer(customerQuery{client: c, users: ms, org: r.Org, customerID: r.CustomerID})
	if err != nil {
		return nil, err
	}
	duplicates, err := listDuplicates(ctx, r)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Org: r.Org, Customer: customerOf(customer), AmountCents: r.AmountCents, Description: r.Description,
		Mode: r.Mode, Stamp: stamp, Duplicates: make([]Invoice, 0, len(duplicates)),
	}
	for _, invoice := range duplicates {
		plan.Duplicates = append(plan.Duplicates, invoiceOf(invoice))
	}
	if len(duplicates) > 0 && !r.AllowDuplicate {
		return nil, &DuplicateError{Plan: plan, raw: duplicates}
	}
	return plan, nil
}

// Execute issues the invoice of a Plan Prepare returned: it stamps the customer when p.Stamp,
// then New → AddLines → Finalize (auto_advance off) → Pay (paid out of band) or Send. A failure is
// an *ExecuteError naming the stage, and, from AddLines onwards, the invoice left behind.
// A plan with no valid Mode is refused (ErrInvalid) before any write.
func Execute(ctx context.Context, c *stripe.Client, p *Plan) (*Result, error) {
	if p.Mode != PaidOutOfBand && p.Mode != Send {
		return nil, fmt.Errorf("%w: the mode must be %q or %q", ErrInvalid, PaidOutOfBand, Send)
	}
	if p.Stamp {
		if err := c.UpdateCustomerMetadata(p.Customer.ID, map[string]string{"address": p.Org.String()}); err != nil {
			return nil, &ExecuteError{Stage: StageStamp, Err: fmt.Errorf(
				"could not stamp customer %s with the organization address: %w", p.Customer.ID, err)}
		}
	}
	params := buildInvoiceParams(p.Org, p.Customer.ID, p.AmountCents, p.Description)
	params.Context = ctx
	invoice, err := stripeinvoice.New(params)
	if err != nil {
		return nil, &ExecuteError{Stage: StageNew, Err: fmt.Errorf("could not create the invoice: %w", err)}
	}
	invoiceID := invoice.ID
	lines := buildLineParams(p.AmountCents, p.Description)
	lines.Context = ctx
	if _, err := stripeinvoice.AddLines(invoiceID, lines); err != nil {
		return nil, &ExecuteError{Stage: StageAddLines, InvoiceID: invoiceID, Err: fmt.Errorf(
			"could not add the line to invoice %s (delete the draft in the dashboard): %w", invoiceID, err)}
	}
	finalize := &stripeapi.InvoiceFinalizeInvoiceParams{AutoAdvance: new(false)}
	finalize.Context = ctx
	if _, err := stripeinvoice.FinalizeInvoice(invoiceID, finalize); err != nil {
		return nil, &ExecuteError{Stage: StageFinalize, InvoiceID: invoiceID, Err: fmt.Errorf(
			"could not finalize invoice %s (delete the draft, or void it if it was finalized, in the dashboard): %w",
			invoiceID, err)}
	}
	switch p.Mode {
	case PaidOutOfBand:
		pay := &stripeapi.InvoicePayParams{PaidOutOfBand: new(true)}
		pay.Context = ctx
		if invoice, err = stripeinvoice.Pay(invoiceID, pay); err != nil {
			return nil, &ExecuteError{Stage: StagePay, InvoiceID: invoiceID, Err: fmt.Errorf(
				"could not mark invoice %s paid out of band (do it in the dashboard): %w", invoiceID, err)}
		}
	case Send:
		send := &stripeapi.InvoiceSendInvoiceParams{}
		send.Context = ctx
		if invoice, err = stripeinvoice.SendInvoice(invoiceID, send); err != nil {
			return nil, &ExecuteError{Stage: StageSend, InvoiceID: invoiceID, Err: fmt.Errorf(
				"could not send invoice %s (send it from the dashboard): %w", invoiceID, err)}
		}
	default:
		// Validate requires one of the two
		return nil, &ExecuteError{Stage: StageFinalize, InvoiceID: invoiceID, Err: fmt.Errorf(
			"invoice %s is finalized but no payment mode was given (void it in the dashboard)", invoiceID)}
	}
	return &Result{Invoice: invoiceOf(invoice)}, nil
}

// OpenManualInvoices returns the organization's manual top-up invoices still open, from the
// customer stamped with it: none when no customer is stamped yet. A customer not stamped yet, or
// an invoice issued seconds ago, may be missing until Stripe's search catches up.
func OpenManualInvoices(c *stripe.Client, org common.Address) ([]Invoice, error) {
	customer, err := c.GetCustomerByAddress(org.String())
	if errors.Is(err, stripe.ErrCustomerNotFound) {
		return []Invoice{}, nil
	}
	if err != nil {
		return nil, err
	}
	invoices := []Invoice{}
	iter := stripeinvoice.List(&stripeapi.InvoiceListParams{
		ListParams: stripeapi.ListParams{Limit: new(int64(100))},
		Customer:   new(customer.ID),
		Status:     new(string(stripeapi.InvoiceStatusOpen)),
	})
	for iter.Next() {
		invoice := iter.Invoice()
		if invoice.Metadata[stripe.MetadataKeyWalletTopUpSource] == stripe.TopUpSourceManual && orgOf(invoice) == org {
			invoices = append(invoices, invoiceOf(invoice))
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("could not list the open invoices of customer %s: %w", customer.ID, err)
	}
	return invoices, nil
}

// InvoiceOfLedgerKey returns the invoice of the top-up ledger row keyed key: the one a manual
// top-up's key names, or the checkout session's. stripe.ErrNoInvoice when there is none yet.
func InvoiceOfLedgerKey(c *stripe.Client, key string) (Invoice, error) {
	invoice, err := c.InvoiceOfLedgerKey(key)
	if err != nil {
		return Invoice{}, err
	}
	return invoiceOf(invoice), nil
}

// orgOf is the organization an invoice's metadata names, the zero address when none.
func orgOf(invoice *stripeapi.Invoice) common.Address {
	raw := invoice.Metadata[stripe.MetadataKeyWalletTopUpOrg]
	if !common.IsHexAddress(raw) {
		return common.Address{}
	}
	return common.HexToAddress(raw)
}

// invoiceOf maps a Stripe invoice onto Invoice.
func invoiceOf(invoice *stripeapi.Invoice) Invoice {
	out := Invoice{
		ID: invoice.ID, Number: invoice.Number, Status: string(invoice.Status),
		AmountCents: pricing.Cents(invoice.TotalExcludingTax), TotalCents: pricing.Cents(invoice.Total),
		HostedURL: invoice.HostedInvoiceURL, PDFURL: invoice.InvoicePDF,
	}
	if invoice.Created != 0 {
		out.Created = time.Unix(invoice.Created, 0).UTC()
	}
	if invoice.Customer != nil {
		out.CustomerID = invoice.Customer.ID
	}
	return out
}

// customerOf maps a Stripe customer onto Customer.
func customerOf(customer *stripeapi.Customer) Customer {
	out := Customer{ID: customer.ID, Name: customer.Name, Email: customer.Email, Address: formatAddress(customer.Address)}
	if customer.Address != nil {
		out.Country = customer.Address.Country
	}
	return out
}

// checkOrganization refuses an organization that has no wallet of its own.
func checkOrganization(org *db.Organization) error {
	if org.ManagedBy != (common.Address{}) {
		return fmt.Errorf("%w: organization %s is managed by %s; top up the integrator instead",
			ErrManagedOrg, org.Address, org.ManagedBy)
	}
	return nil
}

// formatAddress is a customer's billing address on one line, for the operator to check.
func formatAddress(a *stripeapi.Address) string {
	if a == nil {
		return "no address"
	}
	var parts []string
	for _, p := range []string{a.Line1, a.Line2, a.PostalCode, a.City, a.State, a.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// checkCustomer refuses a customer automatic tax cannot locate: one with no address.
func checkCustomer(customer *stripeapi.Customer) error {
	if customer.Address == nil || customer.Address.Country == "" {
		return fmt.Errorf("%w: customer %s has no address, so its tax cannot be computed; complete it in the "+
			"Stripe dashboard first", ErrCustomerNoAddress, customer.ID)
	}
	return nil
}

// buildInvoiceParams is the invoice of a manual top-up of org billed to customerID: sent, not
// charged (send_invoice, auto_advance off), due in DaysUntilDue, EUR with automatic tax, and
// marked manual so the invoice.paid webhook credits it, with the top-up's net amount and
// description in its metadata from creation, so even a draft left with no line is recognized.
func buildInvoiceParams(org common.Address, customerID string, amountCents pricing.Cents, description string,
) *stripeapi.InvoiceParams {
	return &stripeapi.InvoiceParams{
		Customer:         new(customerID),
		Currency:         new(string(stripeapi.CurrencyEUR)),
		CollectionMethod: new(string(stripeapi.InvoiceCollectionMethodSendInvoice)),
		DaysUntilDue:     new(int64(DaysUntilDue)),
		AutoAdvance:      new(false),
		AutomaticTax:     &stripeapi.InvoiceAutomaticTaxParams{Enabled: new(true)},
		Metadata: map[string]string{
			stripe.MetadataKeyWalletTopUpOrg:    org.String(),
			stripe.MetadataKeyWalletTopUpSource: stripe.TopUpSourceManual,
			metadataKeyAmountCents:              strconv.FormatInt(int64(amountCents), 10),
			metadataKeyDescription:              description,
		},
	}
}

// buildLineParams is the invoice's one line: amountCents net of VAT (tax exclusive) for description.
func buildLineParams(amountCents pricing.Cents, description string) *stripeapi.InvoiceAddLinesParams {
	return &stripeapi.InvoiceAddLinesParams{
		Lines: []*stripeapi.InvoiceAddLinesLineParams{{
			Description: new(description),
			Quantity:    new(int64(1)),
			PriceData: &stripeapi.InvoiceAddLinesLinePriceDataParams{
				Currency:    new(string(stripeapi.CurrencyEUR)),
				UnitAmount:  new(int64(amountCents)),
				TaxBehavior: new("exclusive"),
				ProductData: &stripeapi.InvoiceAddLinesLinePriceDataProductDataParams{Name: new(description)},
			},
		}},
	}
}

// duplicateInvoices picks, among invoices of any customer, the manual top-ups of org that are not
// void and whose metadata names the same net amount and description: what issuing r again would
// bill a second time.
func duplicateInvoices(invoices []*stripeapi.Invoice, r Request) []*stripeapi.Invoice {
	var duplicates []*stripeapi.Invoice
	for _, invoice := range invoices {
		if invoice.Metadata[stripe.MetadataKeyWalletTopUpSource] != stripe.TopUpSourceManual ||
			orgOf(invoice) != r.Org || invoice.Status == stripeapi.InvoiceStatusVoid {
			continue
		}
		if invoice.Metadata[metadataKeyAmountCents] == strconv.FormatInt(int64(r.AmountCents), 10) &&
			invoice.Metadata[metadataKeyDescription] == r.Description {
			duplicates = append(duplicates, invoice)
		}
	}
	return duplicates
}

// listDuplicates lists the invoices created within DuplicateWindow, billed to any customer (a
// retry naming another customer of the organization is a duplicate too), and keeps the
// duplicates of r. The invoices are listed, not searched: search lags behind a fresh invoice.
func listDuplicates(ctx context.Context, r Request) ([]*stripeapi.Invoice, error) {
	var invoices []*stripeapi.Invoice
	params := &stripeapi.InvoiceListParams{
		ListParams:   stripeapi.ListParams{Limit: new(int64(100))},
		CreatedRange: &stripeapi.RangeQueryParams{GreaterThanOrEqual: time.Now().Add(-DuplicateWindow).Unix()},
	}
	params.Context = ctx
	iter := stripeinvoice.List(params)
	for iter.Next() {
		invoices = append(invoices, iter.Invoice())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("could not list the invoices of the last 7 days, retry later: %w", err)
	}
	return duplicateInvoices(invoices, r), nil
}
