// Command topup issues a manual wallet top-up for an organization as a Stripe invoice: credit
// sold outside the embedded checkout (a bank transfer, a negotiated amount). The invoice is
// billed to the organization's Stripe customer with VAT by Stripe Tax, and the organization's
// wallet is credited once Stripe reports it paid: the invoice.paid webhook credits it exactly
// once, keyed by the invoice. The command itself never writes the wallet.
//
// The customer is the one GetCustomerByAddress finds for the organization. When none is found
// (customer search is eventually consistent, and a customer a checkout created may not be
// stamped yet), -customer names the one to bill: the customer the organization's checkouts
// created, which holds the billing address and tax id Checkout collected, or, for an
// organization that never checked out, one the operator creates in the Stripe Dashboard with
// the organization's billing address (and tax id) first. It is stamped with the organization
// address so later runs find it, but only with evidence that it is the organization's and not
// another tenant's: a checkout session of the customer for this organization (wallet_topup_org),
// or, for a customer with no checkout session at all, an email that is an Admin of this
// organization and belongs to no other one. A customer whose checkouts were another
// organization's, or whose email could vouch for several, is refused and the operator verifies
// the customer and stamps metadata.address in the Dashboard by hand. A customer is never
// created here: one without an address has no tax location, and automatic tax would fail at
// finalization, so such a customer is refused until the operator completes it in the
// Dashboard. -customer may also name one of several customers stamped with the organization,
// when the search found another.
//
// The invoice is created with collection_method send_invoice, due in 30 days, auto_advance off,
// so Stripe never charges a saved payment method on its own: a run that dies after finalizing
// leaves an open invoice the operator voids or pays by hand. Exactly one mode is given:
//
//	-paid-out-of-band  the money already arrived; the invoice is marked paid out of band
//	-send              the customer pays from Stripe's hosted invoice page (one email)
//
// Keep the Dashboard setting "email finalized invoices" off, or -send emails twice.
//
// Running it again would bill the same top-up twice, so a run is refused when the organization
// already has a manual top-up invoice, billed to any customer, created in the last 7 days and
// not void, for the same net amount and description, which every invoice carries in its
// metadata. The refusal says what to do with each: delete a draft and re-run; an open one needs
// nothing if that run finished, otherwise mark it paid out of band or send it from the Dashboard
// (or void it and re-run); a paid or uncollectible one was already issued. -allow-duplicate
// issues another on purpose.
//
// Usage:
//
//	VOCDONI_STRIPEAPISECRET=sk_... go run ./cmd/topup -org 0x... -amount-cents 50000 \
//	    -description "wallet top-up (bank transfer)" -paid-out-of-band
//	VOCDONI_STRIPEAPISECRET=sk_... go run ./cmd/topup -org 0x... -amount-cents 50000 \
//	    -description "wallet top-up" -send -customer cus_...
//	... -dry-run   # resolve the customer and print what would be created; nothing is written
//
// The MongoDB connection may also be supplied via the VOCDONI_MONGOURL and VOCDONI_MONGODB
// environment variables. The database is only read (the organization and the customer's user),
// never written: it is opened without running migrations and without honouring
// VOCDONI_MONGO_RESET_DB. The Stripe key is read from VOCDONI_STRIPEAPISECRET only, never a flag.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripeinvoice "github.com/stripe/stripe-go/v87/invoice"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// maxAmountCents is the largest top-up, the bound of POST /organizations/{orgAddress}/wallet/topup
// (api.walletTopUpMaxCents); the minimum is pricing.MinimumChargeCents, like every charge.
const maxAmountCents = 100_000_00 // €100 000

// daysUntilDue is how long a sent invoice stays payable.
const daysUntilDue = 30

// duplicateWindow is how far back an invoice for the same top-up refuses a run: long enough to
// catch a retry, short enough that a recurring top-up with the same flags is issued.
const duplicateWindow = 7 * 24 * time.Hour

// Invoice metadata keys that name the top-up a run issued, so a second run recognizes it
// (duplicateInvoices) whatever lines the invoice has.
const (
	metadataKeyAmountCents = "topup_amount_cents"
	metadataKeyDescription = "topup_description"
)

// options holds the parsed CLI flags.
type options struct {
	mongoURL      string
	database      string
	org           string
	amountCents   int64
	description   string
	customer      string
	paidOutOfBand bool
	send          bool
	dryRun        bool
	allowDup      bool
}

func main() {
	var opts options
	flag.StringVar(&opts.mongoURL, "mongoURL", os.Getenv("VOCDONI_MONGOURL"), "MongoDB connection URL (or VOCDONI_MONGOURL)")
	flag.StringVar(&opts.database, "mongoDB", os.Getenv("VOCDONI_MONGODB"), "MongoDB database name (or VOCDONI_MONGODB)")
	flag.StringVar(&opts.org, "org", "", "address of the organization whose wallet is topped up (not a managed one)")
	flag.Int64Var(&opts.amountCents, "amount-cents", 0, "net amount in EUR cents, VAT excluded (€1 to €100 000)")
	flag.StringVar(&opts.description, "description", "", "the invoice line")
	flag.StringVar(&opts.customer, "customer", "", "Stripe customer to bill when none is found by the organization address")
	flag.BoolVar(&opts.paidOutOfBand, "paid-out-of-band", false, "the money already arrived: mark the invoice paid out of band")
	flag.BoolVar(&opts.send, "send", false, "email the invoice; the customer pays from Stripe's hosted page")
	flag.BoolVar(&opts.dryRun, "dry-run", false, "print what would be created and exit without writing anything")
	flag.BoolVar(&opts.allowDup, "allow-duplicate", false,
		"issue the invoice even if the organization got one for the same amount and description in the last 7 days")
	flag.Parse()
	if err := run(&opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// maxDescriptionChars is the longest description: it names the line's product and is kept in the
// invoice metadata, and 250 characters stays within Stripe's limits for both.
const maxDescriptionChars = 250

// validate checks the flags alone: everything that needs no database or Stripe. It trims the
// description in place, so the invoice line and the duplicate check use the same text.
func validate(opts *options) error {
	if opts.mongoURL == "" {
		return fmt.Errorf("-mongoURL is required")
	}
	if !common.IsHexAddress(opts.org) {
		return fmt.Errorf("-org must be an organization address")
	}
	if cents := pricing.Cents(opts.amountCents); cents < pricing.MinimumChargeCents || cents > maxAmountCents {
		return fmt.Errorf("-amount-cents must be between %d and %d eur cents", pricing.MinimumChargeCents, maxAmountCents)
	}
	opts.description = strings.TrimSpace(opts.description)
	if opts.description == "" {
		return fmt.Errorf("-description is required")
	}
	if n := utf8.RuneCountInString(opts.description); n > maxDescriptionChars {
		return fmt.Errorf("-description is %d characters long, at most %d fit (stripe limits)", n, maxDescriptionChars)
	}
	if opts.paidOutOfBand == opts.send {
		return fmt.Errorf("exactly one of -paid-out-of-band or -send is required")
	}
	return nil
}

// checkOrganization refuses an organization that has no wallet of its own.
func checkOrganization(org *db.Organization) error {
	if org.ManagedBy != (common.Address{}) {
		return fmt.Errorf("organization %s is managed by %s and has no wallet; top up the integrator instead",
			org.Address, org.ManagedBy)
	}
	return nil
}

// userStore is the one database read resolveCustomer needs: the user behind a customer's email.
type userStore interface {
	UserByEmail(email string) (*db.User, error)
}

// checkCustomerOrg refuses an unstamped customer the command cannot tie to the organization.
// The evidence is the customer's checkout sessions: one carrying wallet_topup_org of this
// organization accepts it (its checkouts created the customer); sessions of other
// organizations alone refuse it (another tenant's customer). A customer with no checkout
// session at all (one created in the Dashboard) is accepted when its email is an Admin of this
// organization and belongs to no other organization, so the email vouches for one tenant only.
func checkCustomerOrg(q customerQuery, customer *stripeapi.Customer) error {
	sessions, err := q.client.CheckoutSessionsOfCustomer(customer.ID)
	if err != nil {
		return fmt.Errorf("could not list the checkout sessions of customer %s: %w", customer.ID, err)
	}
	if len(sessions) > 0 {
		var orgs []string
		for _, session := range sessions {
			raw := session.Metadata[stripe.MetadataKeyWalletTopUpOrg]
			if common.IsHexAddress(raw) && common.HexToAddress(raw) == q.org {
				return nil
			}
			if raw != "" {
				orgs = append(orgs, raw)
			}
		}
		if len(orgs) == 0 {
			return fmt.Errorf("customer %s has checkout sessions but none a wallet top-up of organization %s, so it is "+
				"not stamped; verify the customer and stamp metadata.address with the organization address in the "+
				"stripe dashboard by hand", customer.ID, q.org)
		}
		return fmt.Errorf("customer %s was created by checkouts of %s, not of organization %s, so it is not stamped",
			customer.ID, strings.Join(orgs, ", "), q.org)
	}
	user, err := q.users.UserByEmail(internal.NormalizeEmail(customer.Email))
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("could not look up the user of customer %s <%s>: %w", customer.ID, customer.Email, err)
	}
	if err != nil || !user.HasRoleFor(q.org, db.AdminRole) {
		return fmt.Errorf("customer %s <%s> is not an admin of organization %s, so it is not stamped; verify the "+
			"customer and stamp metadata.address with the organization address in the stripe dashboard by hand",
			customer.ID, customer.Email, q.org)
	}
	for _, membership := range user.Organizations {
		if membership.Address != q.org {
			return fmt.Errorf("customer %s <%s> is an admin of organization %s but also a member of %s, so its email "+
				"cannot vouch for it and it is not stamped; verify the customer and stamp metadata.address with the "+
				"organization address in the stripe dashboard by hand", customer.ID, customer.Email, q.org, membership.Address)
		}
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
		return fmt.Errorf("customer %s has no address, so its tax cannot be computed; complete it in the Stripe dashboard first",
			customer.ID)
	}
	return nil
}

// buildInvoiceParams is the invoice of a manual top-up of org billed to customer: sent, not
// charged (send_invoice, auto_advance off), due in daysUntilDue, EUR with automatic tax, and
// marked manual so the invoice.paid webhook credits it, with the top-up's net amount and
// description in its metadata from creation, so even a draft left with no line is recognized.
func buildInvoiceParams(org common.Address, customerID string, opts *options) *stripeapi.InvoiceParams {
	return &stripeapi.InvoiceParams{
		Customer:         new(customerID),
		Currency:         new(string(stripeapi.CurrencyEUR)),
		CollectionMethod: new(string(stripeapi.InvoiceCollectionMethodSendInvoice)),
		DaysUntilDue:     new(int64(daysUntilDue)),
		AutoAdvance:      new(false),
		AutomaticTax:     &stripeapi.InvoiceAutomaticTaxParams{Enabled: new(true)},
		Metadata: map[string]string{
			stripe.MetadataKeyWalletTopUpOrg:    org.String(),
			stripe.MetadataKeyWalletTopUpSource: stripe.TopUpSourceManual,
			metadataKeyAmountCents:              strconv.FormatInt(opts.amountCents, 10),
			metadataKeyDescription:              opts.description,
		},
	}
}

// buildLineParams is the invoice's one line: amountCents net of VAT (tax exclusive) for description.
func buildLineParams(amountCents int64, description string) *stripeapi.InvoiceAddLinesParams {
	return &stripeapi.InvoiceAddLinesParams{
		Lines: []*stripeapi.InvoiceAddLinesLineParams{{
			Description: new(description),
			Quantity:    new(int64(1)),
			PriceData: &stripeapi.InvoiceAddLinesLinePriceDataParams{
				Currency:    new(string(stripeapi.CurrencyEUR)),
				UnitAmount:  new(amountCents),
				TaxBehavior: new("exclusive"),
				ProductData: &stripeapi.InvoiceAddLinesLinePriceDataProductDataParams{Name: new(description)},
			},
		}},
	}
}

// duplicateInvoices picks, among invoices of any customer, the manual top-ups of org that are not
// void and whose metadata names the same -amount-cents and -description: what running the
// command again with the same flags would issue a second time.
func duplicateInvoices(invoices []*stripeapi.Invoice, org common.Address, opts *options) []*stripeapi.Invoice {
	var duplicates []*stripeapi.Invoice
	for _, invoice := range invoices {
		rawOrg := invoice.Metadata[stripe.MetadataKeyWalletTopUpOrg]
		if invoice.Metadata[stripe.MetadataKeyWalletTopUpSource] != stripe.TopUpSourceManual ||
			!common.IsHexAddress(rawOrg) || common.HexToAddress(rawOrg) != org ||
			invoice.Status == stripeapi.InvoiceStatusVoid {
			continue
		}
		if invoice.Metadata[metadataKeyAmountCents] == strconv.FormatInt(opts.amountCents, 10) &&
			invoice.Metadata[metadataKeyDescription] == opts.description {
			duplicates = append(duplicates, invoice)
		}
	}
	return duplicates
}

// describeDuplicate is one line of the refusal for an invoice duplicateInvoices picked: the
// invoice, its status and customer, and what the operator does about it, which depends on how far
// the run that issued it got.
func describeDuplicate(invoice *stripeapi.Invoice) string {
	customerID := "unknown"
	if invoice.Customer != nil {
		customerID = invoice.Customer.ID
	}
	var advice string
	switch invoice.Status {
	case stripeapi.InvoiceStatusDraft:
		advice = "delete it in the dashboard and re-run"
	case stripeapi.InvoiceStatusOpen:
		advice = "if that run finished, nothing to do; otherwise mark it paid out of band or send it from the " +
			"dashboard (or void it and re-run)"
	default: // paid or uncollectible
		advice = "already issued, nothing to do"
	}
	return fmt.Sprintf("%s (%s, customer %s): %s", invoice.ID, invoice.Status, customerID, advice)
}

// checkDuplicate refuses the run when the organization already has an invoice for this very
// top-up (duplicateInvoices) created within duplicateWindow, billed to any customer: a retry
// naming another -customer of the organization is a duplicate too. The invoices are listed, not
// searched: search lags behind a fresh invoice.
func checkDuplicate(org common.Address, opts *options) error {
	var invoices []*stripeapi.Invoice
	iter := stripeinvoice.List(&stripeapi.InvoiceListParams{
		ListParams:   stripeapi.ListParams{Limit: new(int64(100))},
		CreatedRange: &stripeapi.RangeQueryParams{GreaterThanOrEqual: time.Now().Add(-duplicateWindow).Unix()},
	})
	for iter.Next() {
		invoices = append(invoices, iter.Invoice())
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("could not list the invoices of the last 7 days, retry later: %w", err)
	}
	duplicates := duplicateInvoices(invoices, org, opts)
	if len(duplicates) == 0 {
		return nil
	}
	lines := make([]string, 0, len(duplicates))
	for _, invoice := range duplicates {
		lines = append(lines, describeDuplicate(invoice))
	}
	return fmt.Errorf("an invoice for the same top-up (organization %s, %d eur cents net, %q) was issued in the last "+
		"7 days: %s; pass -allow-duplicate to issue another on purpose",
		org, opts.amountCents, opts.description, strings.Join(lines, "; "))
}

// run holds the whole CLI flow so os.Exit and flag.Parse stay in main.
func run(opts *options, out io.Writer) error {
	if err := validate(opts); err != nil {
		flag.Usage()
		return err
	}
	apiKey := os.Getenv("VOCDONI_STRIPEAPISECRET")
	if apiKey == "" {
		return fmt.Errorf("VOCDONI_STRIPEAPISECRET environment variable is required")
	}
	orgAddress := common.HexToAddress(opts.org)

	// read only: no reset, no migrations
	ms, err := db.NewWithOptions(opts.mongoURL, opts.database, db.ConnectOptions{SkipMigrations: true})
	if err != nil {
		return fmt.Errorf("could not connect to mongodb: %w", err)
	}
	defer ms.Close()
	org, err := ms.Organization(orgAddress)
	if err != nil {
		return fmt.Errorf("could not find organization %s: %w", orgAddress, err)
	}
	if err := checkOrganization(org); err != nil {
		return err
	}

	client := stripe.NewClient(&stripe.Config{APIKey: apiKey})
	customer, err := resolveCustomer(customerQuery{
		client: client, users: ms, org: orgAddress, customerID: opts.customer, write: !opts.dryRun,
	})
	if err != nil {
		return err
	}
	// a read, so a dry run is refused like the real one would be
	if !opts.allowDup {
		if err := checkDuplicate(orgAddress, opts); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "organization %s, customer %s <%s>\n  %s\n", orgAddress, customer.ID, customer.Email,
		formatAddress(customer.Address)); err != nil {
		return fmt.Errorf("could not print the customer: %w", err)
	}
	if _, err := fmt.Fprintf(out, "invoice: %d eur cents net, %q, send_invoice due in %d days, automatic tax\n",
		opts.amountCents, opts.description, daysUntilDue); err != nil {
		return fmt.Errorf("could not print the invoice: %w", err)
	}
	if opts.dryRun {
		if _, err := fmt.Fprintln(out, "dry run: nothing was created"); err != nil {
			return fmt.Errorf("could not print the dry run result: %w", err)
		}
		return nil
	}

	invoice, err := stripeinvoice.New(buildInvoiceParams(orgAddress, customer.ID, opts))
	if err != nil {
		return fmt.Errorf("could not create the invoice: %w", err)
	}
	invoiceID := invoice.ID
	if _, err := stripeinvoice.AddLines(invoiceID, buildLineParams(opts.amountCents, opts.description)); err != nil {
		return fmt.Errorf("could not add the line to invoice %s (delete the draft in the dashboard): %w", invoiceID, err)
	}
	_, err = stripeinvoice.FinalizeInvoice(invoiceID, &stripeapi.InvoiceFinalizeInvoiceParams{AutoAdvance: new(false)})
	if err != nil {
		return fmt.Errorf("could not finalize invoice %s (delete the draft, or void it if it was finalized, "+
			"in the dashboard): %w", invoiceID, err)
	}
	switch {
	case opts.paidOutOfBand:
		invoice, err = stripeinvoice.Pay(invoiceID, &stripeapi.InvoicePayParams{PaidOutOfBand: new(true)})
		if err != nil {
			return fmt.Errorf("could not mark invoice %s paid out of band (do it in the dashboard): %w", invoiceID, err)
		}
	case opts.send:
		invoice, err = stripeinvoice.SendInvoice(invoiceID, &stripeapi.InvoiceSendInvoiceParams{})
		if err != nil {
			return fmt.Errorf("could not send invoice %s (send it from the dashboard): %w", invoiceID, err)
		}
	default:
		// validate requires exactly one mode
		return fmt.Errorf("invoice %s is finalized but no payment mode was given (void it in the dashboard)", invoiceID)
	}
	// the invoice exists and is paid or sent: a failed print must not hide that
	if _, err := fmt.Fprintf(out, "invoice %s (%s) %s: %d eur cents net, %d with tax\n  %s\n",
		invoice.ID, invoice.Number, invoice.Status, invoice.TotalExcludingTax, invoice.Total,
		invoice.HostedInvoiceURL); err != nil {
		return fmt.Errorf("invoice %s was issued, but could not print it: %w", invoiceID, err)
	}
	if _, err := fmt.Fprintln(out, "the wallet is credited by the invoice.paid webhook once the invoice is paid"); err != nil {
		return fmt.Errorf("invoice %s was issued, but could not print the next step: %w", invoiceID, err)
	}
	return nil
}

// customerQuery is what resolveCustomer needs: the Stripe client, the users to vouch for an
// unstamped customer, the organization, the -customer flag (may be empty) and whether to stamp.
type customerQuery struct {
	client     *stripe.Client
	users      userStore
	org        common.Address
	customerID string
	write      bool
}

// resolveCustomer finds the organization's Stripe customer by its address, or takes customerID
// when none is found and, when write is set, stamps the organization address on it so later
// lookups find it. An unstamped customer is taken only if a checkout of the organization used
// it or, with no checkout at all, its email is an Admin of this organization and of no other
// (checkCustomerOrg). When the search found one and customerID names
// another, that one is used only if it is stamped with the same organization (a duplicate
// stamp), never one stamped with another organization nor an unstamped one. A failed search
// refuses the run: it cannot tell whether a customer is stamped. It never creates a customer,
// refuses one with no address (checkCustomer), and a refused run writes nothing.
func resolveCustomer(q customerQuery) (*stripeapi.Customer, error) {
	client, org, customerID, write := q.client, q.org, q.customerID, q.write
	found, err := client.GetCustomerByAddress(org.String())
	if err != nil && !errors.Is(err, stripe.ErrCustomerNotFound) {
		return nil, fmt.Errorf("could not search the customer of organization %s, retry later: %w", org, err)
	}
	if customerID == "" {
		if found == nil {
			return nil, fmt.Errorf("no stripe customer is stamped with organization %s; pass the one its checkouts "+
				"created with -customer, or create one in the stripe dashboard with the organization's billing "+
				"address (and tax id) first and pass that", org)
		}
		if err := checkCustomer(found); err != nil {
			return nil, err
		}
		return found, nil
	}
	if found != nil && found.ID == customerID {
		if err := checkCustomer(found); err != nil {
			return nil, err
		}
		return found, nil
	}
	customer, err := client.GetCustomer(customerID)
	if err != nil {
		return nil, fmt.Errorf("could not read customer %s: %w", customerID, err)
	}
	stamped := customer.Metadata["address"]
	switch {
	case stamped != "" && common.HexToAddress(stamped) != org:
		return nil, fmt.Errorf("customer %s belongs to organization %s, not %s", customer.ID, stamped, org)
	case stamped == "" && found != nil:
		return nil, fmt.Errorf("organization %s is stamped on customer %s, not %s; drop -customer or fix the stamp "+
			"in the dashboard", org, found.ID, customerID)
	}
	// checked before the stamp: a customer refused here is left as it was
	if err := checkCustomer(customer); err != nil {
		return nil, err
	}
	if stamped == "" {
		if err := checkCustomerOrg(q, customer); err != nil {
			return nil, err
		}
	}
	if write && stamped == "" {
		if err := client.UpdateCustomerMetadata(customer.ID, map[string]string{"address": org.String()}); err != nil {
			return nil, fmt.Errorf("could not stamp customer %s with the organization address: %w", customer.ID, err)
		}
	}
	return customer, nil
}
