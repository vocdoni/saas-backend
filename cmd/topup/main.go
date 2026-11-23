// Command topup issues a manual wallet top-up for an organization as a Stripe invoice: credit
// sold outside the embedded checkout (a bank transfer, a negotiated amount). The checks and the
// invoice are the topup package's, which the staff dashboard runs too. The invoice is
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
// the customer and stamps metadata.address in the Dashboard by hand. The stamp is written only
// when the invoice is issued: a refused or dry run writes nothing in Stripe. A customer is never
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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"github.com/vocdoni/saas-backend/stripe/topup"
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

// validate checks the flags alone and turns them into a top-up request: the CLI's own checks
// (the database, the organization address, exactly one mode), then topup.Validate, which trims
// the description.
func validate(opts *options) (topup.Request, error) {
	if opts.mongoURL == "" {
		return topup.Request{}, fmt.Errorf("-mongoURL is required")
	}
	if !common.IsHexAddress(opts.org) {
		return topup.Request{}, fmt.Errorf("-org must be an organization address")
	}
	if opts.paidOutOfBand == opts.send {
		return topup.Request{}, fmt.Errorf("exactly one of -paid-out-of-band or -send is required")
	}
	mode := topup.Send
	if opts.paidOutOfBand {
		mode = topup.PaidOutOfBand
	}
	return topup.Validate(topup.Request{
		Org: common.HexToAddress(opts.org), AmountCents: pricing.Cents(opts.amountCents), Description: opts.description,
		CustomerID: opts.customer, Mode: mode, AllowDuplicate: opts.allowDup,
	})
}

// run holds the whole CLI flow so os.Exit and flag.Parse stay in main.
func run(opts *options, out io.Writer) error {
	req, err := validate(opts)
	if err != nil {
		flag.Usage()
		return err
	}
	apiKey := os.Getenv("VOCDONI_STRIPEAPISECRET")
	if apiKey == "" {
		return fmt.Errorf("VOCDONI_STRIPEAPISECRET environment variable is required")
	}

	// read only: no reset, no migrations
	ms, err := db.NewWithOptions(opts.mongoURL, opts.database, db.ConnectOptions{SkipMigrations: true})
	if err != nil {
		return fmt.Errorf("could not connect to mongodb: %w", err)
	}
	defer ms.Close()
	ctx := context.Background()
	client := stripe.NewClient(&stripe.Config{APIKey: apiKey})
	plan, err := topup.Prepare(ctx, ms, client, req)
	switch {
	case errors.Is(err, topup.ErrDuplicate):
		return fmt.Errorf("%w; pass -allow-duplicate to issue another on purpose", err)
	case errors.Is(err, topup.ErrNoCustomer):
		return fmt.Errorf("%w (pass it with -customer)", err)
	case err != nil:
		return err
	}
	if _, err := fmt.Fprintf(out, "organization %s, customer %s <%s>\n  %s\n", plan.Org, plan.Customer.ID,
		plan.Customer.Email, plan.Customer.Address); err != nil {
		return fmt.Errorf("could not print the customer: %w", err)
	}
	if _, err := fmt.Fprintf(out, "invoice: %d eur cents net, %q, send_invoice due in %d days, automatic tax\n",
		plan.AmountCents, plan.Description, topup.DaysUntilDue); err != nil {
		return fmt.Errorf("could not print the invoice: %w", err)
	}
	if opts.dryRun {
		if _, err := fmt.Fprintln(out, "dry run: nothing was created"); err != nil {
			return fmt.Errorf("could not print the dry run result: %w", err)
		}
		return nil
	}

	result, err := topup.Execute(ctx, client, plan)
	if err != nil {
		return err
	}
	invoice := result.Invoice
	// the invoice exists and is paid or sent: a failed print must not hide that
	if _, err := fmt.Fprintf(out, "invoice %s (%s) %s: %d eur cents net, %d with tax\n  %s\n",
		invoice.ID, invoice.Number, invoice.Status, invoice.AmountCents, invoice.TotalCents, invoice.HostedURL); err != nil {
		return fmt.Errorf("invoice %s was issued, but could not print it: %w", invoice.ID, err)
	}
	if _, err := fmt.Fprintln(out, "the wallet is credited by the invoice.paid webhook once the invoice is paid"); err != nil {
		return fmt.Errorf("invoice %s was issued, but could not print the next step: %w", invoice.ID, err)
	}
	return nil
}
