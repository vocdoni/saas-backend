package topup

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// MaxReceivedAge is how old the payment of a deferred credit may be: its invoice is due by the
// 15th of the next month, so an older one is a mistake or needs an accountant first.
const MaxReceivedAge = 90 * 24 * time.Hour

// CreditRequest is a direct credit of an organization's wallet for money already received,
// with no Stripe invoice at the time: deferred (the invoice is issued later, IssueDeferredInvoice)
// or External (invoiced by hand outside the platform, never here). A deferred credit names the
// gross received (VAT included, at least AmountCents) and the calendar day it arrived
// (ReceivedAt, midnight UTC); an external one names neither.
type CreditRequest struct {
	Org           common.Address
	AmountCents   pricing.Cents // net, VAT excluded: what the wallet is credited
	ReceivedCents pricing.Cents
	ReceivedAt    time.Time
	Description   string
	External      bool
}

// CreditPlan is a checked direct credit: nothing written.
type CreditPlan struct {
	Org           common.Address `json:"org"`
	AmountCents   pricing.Cents  `json:"amountCents"`
	ReceivedCents pricing.Cents  `json:"receivedCents,omitempty"`
	ReceivedAt    time.Time      `json:"receivedAt,omitzero"`
	Description   string         `json:"description"`
	External      bool           `json:"external"`
}

// PrepareCredit checks a direct credit: Validate's amount and description rules, an organization
// with a wallet of its own, and the received amount and day of a deferred credit (at least the
// amount, a calendar day not in the future, at most MaxReceivedAge old) or their absence for an
// external one. "Not in the future" allows tomorrow's UTC day, today's in time zones ahead of UTC.
// It calls no Stripe and writes nothing; whether a credit is offered at all is the caller's
// decision. Every refusal of the request wraps ErrInvalid.
func PrepareCredit(ms Store, r CreditRequest) (*CreditPlan, error) {
	req, err := Validate(Request{Org: r.Org, AmountCents: r.AmountCents, Description: r.Description, Mode: PaidOutOfBand})
	if err != nil {
		return nil, err
	}
	if r.External {
		if r.ReceivedCents != 0 || !r.ReceivedAt.IsZero() {
			return nil, fmt.Errorf("%w: an external credit names no received amount or date", ErrInvalid)
		}
	} else {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		switch {
		case r.ReceivedCents < r.AmountCents:
			return nil, fmt.Errorf("%w: the gross received (%d eur cents) is below the amount credited (%d)",
				ErrInvalid, r.ReceivedCents, r.AmountCents)
		case r.ReceivedAt.IsZero() || !r.ReceivedAt.Equal(r.ReceivedAt.UTC().Truncate(24*time.Hour)):
			return nil, fmt.Errorf("%w: the received date must be a calendar day", ErrInvalid)
		case r.ReceivedAt.After(today.Add(24 * time.Hour)):
			return nil, fmt.Errorf("%w: the received date is in the future", ErrInvalid)
		case r.ReceivedAt.Before(today.Add(-MaxReceivedAge)):
			return nil, fmt.Errorf("%w: the money was received more than 90 days ago", ErrInvalid)
		}
	}
	org, err := ms.Organization(r.Org)
	if err != nil {
		return nil, fmt.Errorf("could not find organization %s: %w", r.Org, err)
	}
	if err := checkOrganization(org); err != nil {
		return nil, err
	}
	return &CreditPlan{
		Org: r.Org, AmountCents: req.AmountCents, ReceivedCents: r.ReceivedCents, ReceivedAt: r.ReceivedAt.UTC(),
		Description: req.Description, External: r.External,
	}, nil
}

// CreditStore is the database write Credit needs.
type CreditStore interface {
	CreditWallet(c db.WalletCredit) error
}

// Credit credits the wallet of p's organization by p's amount under key, a top-up row: the
// key must be stripe.ManualCreditKeyPrefix (deferred) or stripe.ExternalCreditKeyPrefix
// (external) and a 24-hex id, else ErrInvalid. Crediting the same key again is a no-op, so a
// retry credits at most once.
func Credit(ms CreditStore, p *CreditPlan, key string) error {
	prefix := stripe.ManualCreditKeyPrefix
	if p.External {
		prefix = stripe.ExternalCreditKeyPrefix
	}
	if !stripe.IsCreditKey(key, prefix) {
		return fmt.Errorf("%w: the key of this credit must be %s and a 24-hex id", ErrInvalid, prefix)
	}
	if err := ms.CreditWallet(db.WalletCredit{
		OrgAddress: p.Org, AmountCents: p.AmountCents, IdempotencyKey: key, Kind: db.WalletEntryTopUp,
	}); err != nil {
		return fmt.Errorf("could not credit %s: %w", key, err)
	}
	return nil
}

// ListDuplicates returns the organization's manual top-up invoices of the last 7 days, billed to
// any customer and not void, for the same net amount and description: what Prepare refuses as
// duplicates, for a caller that checks a direct credit against Stripe's top-ups. It only reads.
func ListDuplicates(ctx context.Context, _ *stripe.Client, org common.Address, amountCents pricing.Cents,
	description string,
) ([]Invoice, error) {
	raw, err := recentDuplicates(ctx, Request{Org: org, AmountCents: amountCents, Description: description})
	if err != nil {
		return nil, err
	}
	out := make([]Invoice, 0, len(raw))
	for _, invoice := range raw {
		out = append(out, invoiceOf(invoice))
	}
	return out, nil
}
