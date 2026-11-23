package topup

import (
	"fmt"
	"strings"

	stripeapi "github.com/stripe/stripe-go/v87"
)

// DuplicateError is Prepare refusing a top-up already issued in the last 7 days. Plan is the plan
// that was refused, its Duplicates the invoices found. It wraps ErrDuplicate.
type DuplicateError struct {
	Plan *Plan
	raw  []*stripeapi.Invoice
}

// Error names the refused top-up and, for each duplicate found, what to do with it.
func (e *DuplicateError) Error() string {
	lines := make([]string, 0, len(e.raw))
	for _, invoice := range e.raw {
		lines = append(lines, describeDuplicate(invoice))
	}
	return fmt.Sprintf("an invoice for the same top-up (organization %s, %d eur cents net, %q) was issued in the last "+
		"7 days: %s", e.Plan.Org, e.Plan.AmountCents, e.Plan.Description, strings.Join(lines, "; "))
}

// Unwrap makes a DuplicateError satisfy errors.Is(err, ErrDuplicate).
func (*DuplicateError) Unwrap() error { return ErrDuplicate }

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

// Execute stages, in order; ExecuteError.Stage names the one that failed.
const (
	StageStamp    = "stamp"
	StageNew      = "new"
	StageAddLines = "addLines"
	StageFinalize = "finalize"
	StagePay      = "pay"
	StageSend     = "send"
)

// ExecuteError is Execute failing at Stage. InvoiceID is the invoice left behind (a draft or an
// open invoice the operator deletes or voids in Stripe), set from StageAddLines onwards.
type ExecuteError struct {
	Stage     string
	InvoiceID string
	Err       error
}

// Error is the message of the error the failed stage returned.
func (e *ExecuteError) Error() string { return e.Err.Error() }

// Unwrap returns the error the failed stage returned.
func (e *ExecuteError) Unwrap() error { return e.Err }
