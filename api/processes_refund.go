package api

import (
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// refundPaidProcess returns a paid draft's money the way it arrived and records that it did:
// the payment row survives as the audit trail of a process that no longer exists, which is
// why the caller must not delete it. The organization's branding add-on is released with the
// refund, so its next draft is quoted — and charged — for what the deleted one paid. Unless
// another process already rides on that branding: then the branding is withheld from the
// refund and stays the organization's, since the add-on it bought is in use.
//
// What to withhold is fixed on the payment before any money moves (PlanProcessPaymentRefund),
// and a retry reuses it: recomputing it could flip once a sibling started or stopped relying
// on the branding, and the retry would then refund under other keys than the money that
// already went back. The plan also locks the payment, so no census growth lands mid-refund.
//
// Reports false with the response already written when the money could not be returned.
// The draft stays then, delete-only, so the delete can be retried; destroying a draft whose
// money is still with us is the one outcome this must never produce.
//
// ponytail: a sibling that pays between the check and the refund still gets branding the
// organization no longer holds; its publish gate re-prices it, so the gap is a 402, not a loss.
func (a *API) refundPaidProcess(w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment) bool {
	withheldCents, ok := a.planPaidProcessRefund(w, vp, payment)
	if !ok {
		return false
	}
	keepBranding := withheldCents > 0
	refundID, err := a.returnProcessPayment(vp, payment, withheldCents)
	if err != nil {
		writeSubscriptionError(w, err)
		return false
	}
	marked, err := a.db.MarkProcessPaymentRefunded(vp.ID, refundID, payment.AmountCents)
	if err != nil {
		// the money is already back; losing the status write would let a retry refund
		// twice, so this is reported rather than swallowed
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return false
	}
	if !marked {
		// the plan locks the envelope, so this is only a guard: a payment that moved anyway
		// is not recorded as refunded, and the draft stays for a retry
		errors.ErrPaymentSessionConflict.
			Withf("the process payment changed while it was being refunded; retry the delete").Write(w)
		return false
	}
	if payment.Branding && !keepBranding {
		if err := a.db.ReleaseOrganizationBranding(vp.OrgAddress, vp.ID); err != nil {
			log.Warnw("could not release organization branding after refund",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "error", err)
		}
	}
	log.Infow("paid draft refunded", "processId", vp.ID.Hex(), "amountCents", payment.AmountCents,
		"refundId", refundID, "wallet", payment.CheckoutSessionID == "", "brandingWithheld", keepBranding)
	return true
}

// planPaidProcessRefund returns what the refund withholds: the plan an earlier attempt fixed,
// or a new one — the branding add-on when another process relies on it, else nothing — fixed
// on the payment before the caller moves money. Reports false with the response written.
func (a *API) planPaidProcessRefund(w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment) (int64, bool) {
	if payment.RefundWithheldCents != nil {
		return *payment.RefundWithheldCents, true
	}
	var withheldCents int64
	if payment.Branding {
		reliedOn, err := a.db.OrganizationBrandingReliedOn(vp.OrgAddress, vp.ID)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return 0, false
		}
		if reliedOn {
			withheldCents = pricing.BrandingCents
		}
	}
	planned, err := a.db.PlanProcessPaymentRefund(vp.ID, payment.AmountCents, withheldCents)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return 0, false
	}
	if !planned {
		// nothing has moved: a census top-up raised the payment after it was read
		errors.ErrPaymentSessionConflict.
			Withf("the process payment changed while it was being refunded; retry the delete").Write(w)
		return 0, false
	}
	return withheldCents, true
}

// refuseRefundedDraft refuses a draft a delete started refunding and did not finish dropping:
// its money is back, or its refund is fixed and on its way, so finishing that delete is all it
// can do. Publishing it again would be free, since the wallet debit is keyed per (process,
// price) and the original key stays applied after its refund; growing it would buy voters its
// refund does not return. A payment state it cannot read is returned, never taken as absent.
func (a *API) refuseRefundedDraft(processID bson.ObjectID) error {
	payment, err := a.db.ProcessPayment(processID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	if payment.Status == db.ProcessPaymentRefunded || payment.RefundWithheldCents != nil {
		return errors.ErrPaymentSessionConflict.
			Withf("the process is refunded by a delete that did not finish; delete it again")
	}
	return nil
}

// returnProcessPayment moves the money back and returns the id the refund is known by. A
// card payment is refunded against its payment intent, VAT included; a wallet-paid process
// credits the integrator wallet that was debited, keyed so a retried delete cannot credit
// twice. A card-paid process may also hold census top-ups, each its own payment intent, and
// every one of them is refunded; the returned id is the first payment's refund.
//
// The wallet credit covers AmountCents, which already includes wallet-paid census growth; the
// refund plan keeps it from growing further, so one key per process is enough.
//
// withheldCents is a net amount kept back (the relied-on branding add-on): taken from a wallet
// credit as is, and from a card refund grossed up by the first charge's own VAT ratio.
//
// Nothing is written to the response: an error is an errors.Error carrying its HTTP status,
// or a plain one meaning 500 — write it with writeSubscriptionError.
func (a *API) returnProcessPayment(
	vp *db.VotingProcess, payment *db.ProcessPayment, withheldCents int64,
) (string, error) {
	idempotencyKey := "refund:" + vp.ID.Hex()
	if payment.CheckoutSessionID == "" {
		// paid from the integrator wallet: the money goes back where it came from, so the
		// integrator that funded the draft can spend it on the next one
		org, err := a.db.Organization(vp.OrgAddress)
		if err != nil {
			return "", fmt.Errorf("failed to get organization: %w", err)
		}
		if org.ManagedBy == (common.Address{}) {
			return "", fmt.Errorf("process %s was paid from a wallet but its organization has no integrator", vp.ID.Hex())
		}
		amount := payment.AmountCents - withheldCents
		if amount <= 0 {
			return idempotencyKey, nil // the branding was all it paid for, and it stays
		}
		if err := a.db.CreditWallet(db.WalletCredit{
			OrgAddress:     org.ManagedBy,
			AmountCents:    amount,
			IdempotencyKey: idempotencyKey,
			Kind:           db.WalletEntryRefund,
			ProcessID:      vp.ID,
		}); err != nil {
			return "", fmt.Errorf("failed to credit the refund to the wallet: %w", err)
		}
		return idempotencyKey, nil
	}
	if a.paymentGW == nil {
		return "", errors.ErrPaymentSessionConflict.
			Withf("the payment gateway is unavailable; the payment cannot be refunded")
	}
	var firstAmount int64 // zero refunds the whole intent
	if withheldCents > 0 {
		if payment.ChargeSubtotalCents <= 0 {
			return "", fmt.Errorf("process %s has no recorded charge to withhold branding from", vp.ID.Hex())
		}
		// the withheld net grossed up by the first charge's VAT ratio, rounded half up
		withheld := (withheldCents*payment.ChargeTotalCents + payment.ChargeSubtotalCents/2) /
			payment.ChargeSubtotalCents
		firstAmount = payment.ChargeTotalCents - withheld
	}
	var refundID string
	for i, intent := range append([]string{payment.PaymentIntentID}, payment.TopUpIntents...) {
		amount := int64(0) // top-ups never carry branding, so they come back whole
		if i == 0 && withheldCents > 0 {
			if firstAmount <= 0 {
				refundID = "withheld:" + intent // the branding was all it paid for, and it stays
				continue
			}
			amount = firstAmount
		}
		refund, err := a.paymentGW.RefundProcessPayment(vp.ID, intent, amount)
		if err != nil {
			return "", errors.ErrStripeError.Withf("could not refund the process payment").WithErr(err)
		}
		if refundID == "" {
			refundID = refund.ID
		}
	}
	return refundID, nil
}
