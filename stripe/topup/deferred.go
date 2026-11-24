package topup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripecustomer "github.com/stripe/stripe-go/v87/customer"
	stripeinvoice "github.com/stripe/stripe-go/v87/invoice"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// IssueDeferredInvoice stages, besides Execute's: finding the invoice a previous attempt left
// (lookup), deleting its draft (delete), recording a new invoice's id (record) and the finalized
// total that is not the gross received (total).
const (
	StageLookup = "lookup"
	StageDelete = "delete"
	StageRecord = "record"
	StageTotal  = "total"
)

// DeferredInvoice is the invoice of a manual credit, issued after the wallet was credited:
// LedgerKey is the credit's key (stripe.ManualCreditKeyPrefix), AmountCents its net,
// ReceivedCents the gross received (the invoice's total must match it) and ReceivedAt the day it
// arrived. CustomerID names the customer when the search may not find it yet (it was just
// registered), else empty. InvoiceID is the invoice a previous attempt created, and OnCreated
// stores a new invoice's id before anything else is written to it.
type DeferredInvoice struct {
	Org           common.Address
	CustomerID    string
	LedgerKey     string
	AmountCents   pricing.Cents
	ReceivedCents pricing.Cents
	Description   string
	ReceivedAt    time.Time
	InvoiceID     string
	OnCreated     func(invoiceID string) error
}

// IssueDeferredInvoice issues, or finishes issuing, the invoice of a manual credit: billed to
// the organization's customer (stamped with it, with an address), source TopUpSourceDeferred
// with the ledger key in its metadata (the invoice.paid webhook ignores it: the wallet was
// credited already), one line "<description> (pago recibido el <day>)", finalized and marked
// paid out of band. It resumes what a previous attempt left, never creating a second invoice
// with a line: the invoice named by InvoiceID, else the one of the customer's invoices (listed,
// not searched) that carries the ledger key and is not void. A paid one is returned as is, an
// open one is only paid, a draft is deleted and created again, a void or deleted one created
// again, and an uncollectible one, or an open one for another net or customer, refused
// (ErrDuplicate). An InvoiceID whose invoice is not this credit's deferred invoice (another
// ledger key or source) is refused (ErrInvalid) before anything is written. A finalized total
// other than ReceivedCents is never paid (stage total): the invoice stays open for the operator to
// void, after fixing the customer's tax data, so the next attempt creates it again. Failures past
// the customer are *ExecuteError with the stage and the invoice left behind.
//
// The create's Idempotency-Key is the ledger key, the invoice it replaces (the draft it deleted or
// the void or deleted one it skipped, "first" when none) and the customer; the line's is the
// invoice's id, and an invoice found finalized or paid by another call is paid or returned as a
// resume would. So two concurrent calls from the same state share one invoice. Callers should
// still serialize the calls per ledger key: the keys make a race produce one invoice, not a
// tidy one (a call may fail while the other issues it).
func IssueDeferredInvoice(ctx context.Context, c *stripe.Client, in DeferredInvoice) (*Result, error) {
	if !stripe.IsCreditKey(in.LedgerKey, stripe.ManualCreditKeyPrefix) || in.ReceivedCents < in.AmountCents ||
		in.ReceivedAt.IsZero() || in.OnCreated == nil {
		return nil, fmt.Errorf("%w: a deferred invoice needs a manual credit key, the gross received, its date and "+
			"OnCreated", ErrInvalid)
	}
	customer, err := deferredCustomer(ctx, c, in.Org, in.CustomerID)
	if err != nil {
		return nil, err
	}
	existing, replaced, err := previousDeferred(ctx, customer.ID, in)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch existing.Status {
		case stripeapi.InvoiceStatusPaid:
			return &Result{Invoice: invoiceOf(existing)}, nil
		case stripeapi.InvoiceStatusOpen:
			if existing.Customer == nil || existing.Customer.ID != customer.ID ||
				pricing.Cents(existing.TotalExcludingTax) != in.AmountCents {
				return nil, fmt.Errorf("%w: invoice %s of %s is open for another customer or net amount; void it in the "+
					"dashboard and retry", ErrDuplicate, existing.ID, in.LedgerKey)
			}
			return payDeferred(ctx, existing, in)
		case stripeapi.InvoiceStatusDraft:
			params := &stripeapi.InvoiceParams{}
			params.Context = ctx
			if _, err := stripeinvoice.Del(existing.ID, params); err != nil {
				return nil, &ExecuteError{Stage: StageDelete, InvoiceID: existing.ID, Err: fmt.Errorf(
					"could not delete draft %s left by a previous attempt: %w", existing.ID, err)}
			}
			replaced = existing.ID
		default: // uncollectible
			return nil, fmt.Errorf("%w: invoice %s of %s is %s; void it in the dashboard and retry",
				ErrDuplicate, existing.ID, in.LedgerKey, existing.Status)
		}
	}

	params := buildInvoiceParams(in.Org, customer.ID, in.AmountCents, in.Description)
	params.Metadata[stripe.MetadataKeyWalletTopUpSource] = stripe.TopUpSourceDeferred
	params.Metadata[stripe.MetadataKeyWalletLedgerKey] = in.LedgerKey
	params.Context = ctx
	params.SetIdempotencyKey(fmt.Sprintf("topup-deferred:%s:%s:%s", in.LedgerKey, cmp.Or(replaced, "first"), customer.ID))
	invoice, err := stripeinvoice.New(params)
	if err != nil {
		return nil, &ExecuteError{Stage: StageNew, Err: fmt.Errorf("could not create the invoice: %w", err)}
	}
	if err := in.OnCreated(invoice.ID); err != nil {
		return nil, &ExecuteError{Stage: StageRecord, InvoiceID: invoice.ID, Err: fmt.Errorf(
			"could not record draft %s; the next attempt deletes it: %w", invoice.ID, err)}
	}
	lines := buildLineParams(in.AmountCents, in.Description)
	lines.Lines[0].Description = new(fmt.Sprintf("%s (pago recibido el %s)", in.Description,
		in.ReceivedAt.UTC().Format(time.DateOnly)))
	lines.Context = ctx
	lines.SetIdempotencyKey("topup-deferred-lines:" + invoice.ID)
	if _, err := stripeinvoice.AddLines(invoice.ID, lines); err != nil {
		return nil, &ExecuteError{Stage: StageAddLines, InvoiceID: invoice.ID, Err: fmt.Errorf(
			"could not add the line to invoice %s; the next attempt deletes the draft: %w", invoice.ID, err)}
	}
	finalize := &stripeapi.InvoiceFinalizeInvoiceParams{AutoAdvance: new(false)}
	finalize.Context = ctx
	finalized, err := stripeinvoice.FinalizeInvoice(invoice.ID, finalize)
	if err != nil {
		// another call with the same keys may have finalized it, or paid it, already
		switch live := liveInvoice(ctx, invoice.ID); {
		case live != nil && live.Status == stripeapi.InvoiceStatusPaid:
			return &Result{Invoice: invoiceOf(live)}, nil
		case live != nil && live.Status == stripeapi.InvoiceStatusOpen:
			return payDeferred(ctx, live, in)
		}
		return nil, &ExecuteError{Stage: StageFinalize, InvoiceID: invoice.ID, Err: fmt.Errorf(
			"could not finalize invoice %s (an address stripe tax cannot locate?); the next attempt deletes the draft: %w",
			invoice.ID, err)}
	}
	return payDeferred(ctx, finalized, in)
}

// liveInvoice is the invoice id as Stripe has it now, nil when it cannot be read.
func liveInvoice(ctx context.Context, id string) *stripeapi.Invoice {
	params := &stripeapi.InvoiceParams{}
	params.Context = ctx
	invoice, err := stripeinvoice.Get(id, params)
	if err != nil {
		return nil
	}
	return invoice
}

// payDeferred marks an open deferred invoice paid out of band, unless its total is not the gross
// received (stage total, nothing written). An invoice another call paid meanwhile is returned.
func payDeferred(ctx context.Context, invoice *stripeapi.Invoice, in DeferredInvoice) (*Result, error) {
	if pricing.Cents(invoice.Total) != in.ReceivedCents {
		return nil, &ExecuteError{Stage: StageTotal, InvoiceID: invoice.ID, Err: fmt.Errorf(
			"invoice %s totals %d eur cents but %d were received: it is left open and not paid; fix the customer's tax "+
				"data and void it, then retry, or settle it by hand", invoice.ID, invoice.Total, in.ReceivedCents)}
	}
	pay := &stripeapi.InvoicePayParams{PaidOutOfBand: new(true)}
	pay.Context = ctx
	paid, err := stripeinvoice.Pay(invoice.ID, pay)
	if err != nil {
		// another call may have paid it already
		if live := liveInvoice(ctx, invoice.ID); live != nil && live.Status == stripeapi.InvoiceStatusPaid {
			return &Result{Invoice: invoiceOf(live)}, nil
		}
		return nil, &ExecuteError{Stage: StagePay, InvoiceID: invoice.ID, Err: fmt.Errorf(
			"could not mark invoice %s paid out of band; retry: %w", invoice.ID, err)}
	}
	return &Result{Invoice: invoiceOf(paid)}, nil
}

// deferredCustomer is the customer a deferred invoice bills: customerID when given (just
// registered, the search may not find it yet), else the one stamped with org. It must be stamped
// with org and have an address. Unlike resolveCustomer, an unstamped customer is never taken. A
// customerID Stripe does not know (resource_missing) is refused (ErrInvalid): no retry finds it.
func deferredCustomer(ctx context.Context, c *stripe.Client, org common.Address, customerID string,
) (*stripeapi.Customer, error) {
	var customer *stripeapi.Customer
	var err error
	if customerID != "" {
		params := &stripeapi.CustomerParams{}
		params.Context = ctx
		customer, err = stripecustomer.Get(customerID, params)
		var stripeErr *stripeapi.Error
		if errors.As(err, &stripeErr) && stripeErr.Code == stripeapi.ErrorCodeResourceMissing {
			return nil, fmt.Errorf("%w: customer %s does not exist", ErrInvalid, customerID)
		}
	} else {
		customer, err = c.GetCustomerByAddress(org.String())
		if errors.Is(err, stripe.ErrCustomerNotFound) {
			return nil, fmt.Errorf("%w is stamped with organization %s; register it first", ErrNoCustomer, org)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("could not read the customer of organization %s, retry later: %w", org, err)
	}
	if stamped := customer.Metadata["address"]; !common.IsHexAddress(stamped) || common.HexToAddress(stamped) != org {
		return nil, fmt.Errorf("%w: customer %s is not stamped with organization %s", ErrCustomerOtherOrg, customer.ID, org)
	}
	return customer, checkCustomer(customer)
}

// previousDeferred is the invoice a previous attempt left for in.LedgerKey, nil when none: the one
// in.InvoiceID names unless it is void or deleted, else the first of the customer's invoices,
// listed (search lags), that carries the key and is not void. replaced is the void or deleted
// invoice skipped (in.InvoiceID's, else the newest listed void one), empty when none. An
// in.InvoiceID whose invoice is not this credit's deferred invoice is refused (ErrInvalid).
func previousDeferred(ctx context.Context, customerID string, in DeferredInvoice,
) (existing *stripeapi.Invoice, replaced string, err error) {
	if in.InvoiceID != "" {
		params := &stripeapi.InvoiceParams{}
		params.Context = ctx
		invoice, err := stripeinvoice.Get(in.InvoiceID, params)
		var stripeErr *stripeapi.Error
		switch {
		case errors.As(err, &stripeErr) && stripeErr.Code == stripeapi.ErrorCodeResourceMissing:
			replaced = in.InvoiceID // deleted: look for another
		case err != nil:
			return nil, "", &ExecuteError{Stage: StageLookup, InvoiceID: in.InvoiceID, Err: fmt.Errorf(
				"could not read invoice %s, retry later: %w", in.InvoiceID, err)}
		case invoice.Metadata[stripe.MetadataKeyWalletLedgerKey] != in.LedgerKey ||
			invoice.Metadata[stripe.MetadataKeyWalletTopUpSource] != stripe.TopUpSourceDeferred:
			return nil, "", fmt.Errorf("%w: invoice %s does not belong to %s", ErrInvalid, in.InvoiceID, in.LedgerKey)
		case invoice.Status != stripeapi.InvoiceStatusVoid:
			return invoice, "", nil
		default:
			replaced = in.InvoiceID // void: look for another
		}
	}
	params := &stripeapi.InvoiceListParams{ListParams: stripeapi.ListParams{Limit: new(int64(100))}, Customer: new(customerID)}
	params.Context = ctx
	iter := stripeinvoice.List(params)
	for iter.Next() {
		invoice := iter.Invoice()
		if invoice.Metadata[stripe.MetadataKeyWalletLedgerKey] != in.LedgerKey {
			continue
		}
		if invoice.Status != stripeapi.InvoiceStatusVoid {
			return invoice, replaced, nil
		}
		replaced = cmp.Or(replaced, invoice.ID)
	}
	if err := iter.Err(); err != nil {
		return nil, "", &ExecuteError{Stage: StageLookup, Err: fmt.Errorf(
			"could not list the invoices of customer %s, retry later: %w", customerID, err)}
	}
	return nil, replaced, nil
}
