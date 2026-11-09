package stripe

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
)

// Wallet top-up invoices are Stripe's own: a checkout top-up's is the one Checkout issues for
// the session, a manual top-up's (cmd/topup) is created and finalized by the command. The ledger
// row's idempotency key names the invoice — the session id, or InvoiceTopUpKey — so the API
// serves its PDF without storing anything new, and a manual invoice is credited once when Stripe
// reports it paid.

// invoiceTopUpKeyPrefix starts the idempotency key of a manual top-up (InvoiceTopUpKey).
const invoiceTopUpKeyPrefix = "invoice:"

// InvoiceTopUpKey is the idempotency key of the wallet credit of a manual top-up invoice: once
// per invoice, whatever events Stripe replays.
func InvoiceTopUpKey(invoiceID string) string { return invoiceTopUpKeyPrefix + invoiceID }

// handleInvoicePaid credits the wallet of a manual top-up invoice (TopUpSourceManual) once Stripe
// reports it paid, through a payment or marked paid out of band. Every other invoice — a checkout
// top-up's (credited by the checkout events), a subscription's — is ignored. The event is trusted
// only for the invoice id, the source marker and the organization the invoice was issued for:
// the credit itself is creditManualInvoice's, from the invoice as Stripe has it now. A manual
// invoice whose payload names no valid organization is dropped as permanent, to be credited by hand.
func (s *Service) handleInvoicePaid(event *stripeapi.Event) error {
	var invoice stripeapi.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return fmt.Errorf("failed to parse invoice from event %s: %v: %w", event.ID, err, errPermanentEvent)
	}
	if invoice.Metadata[MetadataKeyWalletTopUpSource] != TopUpSourceManual {
		return nil
	}
	if invoice.ID == "" {
		return fmt.Errorf("invoice event %s has no invoice id: %w", event.ID, errPermanentEvent)
	}
	rawOrg := invoice.Metadata[MetadataKeyWalletTopUpOrg]
	if !common.IsHexAddress(rawOrg) {
		return fmt.Errorf("invalid organization address %q in the payload of invoice %s, credit it by hand: %w",
			rawOrg, invoice.ID, errPermanentEvent)
	}
	issuedOrg := common.HexToAddress(rawOrg)
	return s.creditManualInvoice(invoice.ID, &issuedOrg)
}

// creditManualInvoice credits the wallet of the manual top-up invoice invoiceID from its live
// state, re-fetched as Stripe recommends: nothing unless it is manual (TopUpSourceManual) and
// paid. The net credited is total_excluding_tax (after discounts, before tax), keyed
// InvoiceTopUpKey so crediting it again, from any event, is a no-op, with the payment intent a
// refund names when the invoice was paid through Stripe (none out of band) and amount_due — what
// that payment collected, the total plus any debit balance paid on top — as the gross, so a
// refund of the payment is mirrored at the net/collected ratio. Two invoices are refused for good,
// to be credited by hand: one whose amount_due is below its total (a customer credit balance was
// applied, so less than the total was collected; a debit balance paid on top is fine, the total
// was collected), and, when issuedOrg is given (the organization in an invoice.paid payload), one
// whose organization was edited since (the live wallet_topup_org differs), since the credit is
// keyed per wallet and would land twice. A refund handled before invoice.paid credits through it
// with no issuedOrg (topUpOfPaymentIntent): it has no payload to tell an edited organization by,
// and the invoice.paid of an edited one was refused and credited by hand. So it requires the
// invoice's customer to be stamped with the same organization (metadata.address, as cmd/topup
// only bills such a customer), and refuses the invoice for good otherwise.
func (s *Service) creditManualInvoice(invoiceID string, issuedOrg *common.Address) error {
	live, err := s.client.GetInvoice(invoiceID)
	if err != nil {
		return fmt.Errorf("failed to re-read invoice %s: %w", invoiceID, err)
	}
	if live.Metadata[MetadataKeyWalletTopUpSource] != TopUpSourceManual {
		return nil
	}
	if live.Status != stripeapi.InvoiceStatusPaid {
		log.Infow("manual top-up invoice no longer paid, nothing to credit", "invoiceId", live.ID, "status", live.Status)
		return nil
	}
	rawOrg := live.Metadata[MetadataKeyWalletTopUpOrg]
	if !common.IsHexAddress(rawOrg) {
		return fmt.Errorf("invalid organization address %q in invoice %s metadata: %w", rawOrg, live.ID, errPermanentEvent)
	}
	orgAddress := common.HexToAddress(rawOrg)
	if issuedOrg != nil && *issuedOrg != orgAddress {
		return fmt.Errorf("invoice %s was issued for organization %s and now names %s, credit it by hand: %w",
			live.ID, *issuedOrg, orgAddress, errPermanentEvent)
	}
	if issuedOrg == nil {
		if err := s.checkInvoiceCustomerOrg(live, orgAddress); err != nil {
			return err
		}
	}
	if live.AmountDue < live.Total {
		return fmt.Errorf("invoice %s has amount_due %d below its total %d (customer balance %d applied), credit it by hand: %w",
			live.ID, live.AmountDue, live.Total, live.StartingBalance, errPermanentEvent)
	}
	org, err := s.db.Organization(orgAddress)
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("organization %s of invoice %s not found: %w", orgAddress, live.ID, errPermanentEvent)
	}
	if err != nil {
		return fmt.Errorf("failed to get organization %s of invoice %s: %w", orgAddress, live.ID, err)
	}
	if org.ManagedBy != (common.Address{}) {
		return fmt.Errorf("organization %s of invoice %s is managed and has no wallet: %w",
			orgAddress, live.ID, errPermanentEvent)
	}
	if live.Currency != stripeapi.CurrencyEUR {
		return fmt.Errorf("invoice %s is in %s, not eur: %w", live.ID, live.Currency, errPermanentEvent)
	}
	net := pricing.Cents(live.TotalExcludingTax)
	if net <= 0 {
		log.Warnw("manual top-up invoice paid for nothing net, nothing credited", "invoiceId", live.ID, "netCents", net)
		return nil
	}
	if err := s.db.CreditWallet(db.WalletCredit{
		OrgAddress:      orgAddress,
		AmountCents:     net,
		IdempotencyKey:  InvoiceTopUpKey(live.ID),
		Kind:            db.WalletEntryTopUp,
		PaymentIntentID: invoicePaymentIntentID(live),
		GrossCents:      pricing.Cents(live.AmountDue),
	}); err != nil {
		return fmt.Errorf("failed to credit manual top-up invoice %s: %w", live.ID, err)
	}
	log.Infow("manual top-up invoice credited", "orgAddress", orgAddress.String(), "amountCents", net, "invoiceId", live.ID)
	return nil
}

// checkInvoiceCustomerOrg refuses for good an invoice whose customer is not stamped with
// orgAddress, the organization the invoice names: none, unstamped, or stamped with another. A
// customer Stripe fails to serve is retried.
func (s *Service) checkInvoiceCustomerOrg(invoice *stripeapi.Invoice, orgAddress common.Address) error {
	var customerID, stamped string
	if invoice.Customer != nil && invoice.Customer.ID != "" {
		customerID = invoice.Customer.ID
		customer, err := s.client.GetCustomer(customerID)
		if err != nil {
			return fmt.Errorf("failed to read customer %s of invoice %s: %w", customerID, invoice.ID, err)
		}
		stamped = customer.Metadata["address"]
	}
	if !common.IsHexAddress(stamped) || common.HexToAddress(stamped) != orgAddress {
		return fmt.Errorf("invoice %s names organization %s but its customer %s carries %q: the returned funds are not "+
			"mirrored, debit them by hand (invoice.paid still credits the invoice unless it refuses it): %w",
			invoice.ID, orgAddress, customerID, stamped, errPermanentEvent)
	}
	return nil
}

// invoicePaymentIntentID is the payment intent that paid invoice: the first paid payment of
// type payment_intent among its (expanded) payments, or empty for one paid out of band.
func invoicePaymentIntentID(invoice *stripeapi.Invoice) string {
	if invoice.Payments == nil {
		return ""
	}
	for _, p := range invoice.Payments.Data {
		if p == nil || p.Status != "paid" || p.Payment == nil || p.Payment.PaymentIntent == nil {
			continue
		}
		if p.Payment.Type == stripeapi.InvoicePaymentPaymentTypePaymentIntent {
			return p.Payment.PaymentIntent.ID
		}
	}
	return ""
}
