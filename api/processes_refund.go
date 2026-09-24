package api

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
)

// refundPaidProcess returns a paid draft's money the way it arrived and records that it did:
// the payment row survives as the audit trail of a process that no longer exists, which is
// why the caller must not delete it. The organization's branding add-on is released with the
// refund, so its next draft is quoted — and charged — for what the deleted one paid. Unless
// another process already rides on that branding: then the branding is withheld from the
// refund and stays the organization's, since the add-on it bought is in use.
//
// Reports false with the response already written when the money could not be returned.
// Nothing is recorded then and the draft stays, so the delete can be retried; destroying a
// draft whose money is still with us is the one outcome this must never produce.
//
// ponytail: a sibling that pays between the check and the refund still gets branding the
// organization no longer holds; its publish gate re-prices it, so the gap is a 402, not a loss.
func (a *API) refundPaidProcess(w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment) bool {
	keepBranding := false
	if payment.Branding {
		reliedOn, err := a.db.OrganizationBrandingReliedOn(vp.OrgAddress, vp.ID)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return false
		}
		keepBranding = reliedOn
	}
	var withheldCents int64
	if keepBranding {
		withheldCents = pricing.BrandingCents
	}
	refundID, ok := a.returnProcessPayment(w, vp, payment, withheldCents)
	if !ok {
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
		// a census top-up landed after the payment was read, so its intent was not refunded
		// yet; the draft stays and a retry refunds it (the ones already returned are keyed)
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

// returnProcessPayment moves the money back and returns the id the refund is known by. A
// card payment is refunded against its payment intent, VAT included; a wallet-paid process
// credits the integrator wallet that was debited, keyed so a retried delete cannot credit
// twice. A card-paid process may also hold census top-ups, each its own payment intent, and
// every one of them is refunded; the returned id is the first payment's refund.
//
// ponytail: the wallet credit covers AmountCents, which already includes wallet-paid census
// growth. A wallet growth landing between this credit and the refunded mark is not credited:
// the retry reuses the same key. Key the credit per amount if managed drafts ever grow
// concurrently with their own delete.
//
// withheldCents is a net amount kept back (the relied-on branding add-on): taken from a wallet
// credit as is, and from a card refund grossed up by the first charge's own VAT ratio.
func (a *API) returnProcessPayment(
	w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment, withheldCents int64,
) (string, bool) {
	idempotencyKey := "refund:" + vp.ID.Hex()
	if payment.CheckoutSessionID == "" {
		// paid from the integrator wallet: the money goes back where it came from, so the
		// integrator that funded the draft can spend it on the next one
		org, err := a.db.Organization(vp.OrgAddress)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return "", false
		}
		if org.ManagedBy == (common.Address{}) {
			errors.ErrGenericInternalServerError.
				Withf("process %s was paid from a wallet but its organization has no integrator", vp.ID.Hex()).Write(w)
			return "", false
		}
		amount := payment.AmountCents - withheldCents
		if amount <= 0 {
			return idempotencyKey, true // the branding was all it paid for, and it stays
		}
		if err := a.db.CreditWallet(db.WalletCredit{
			OrgAddress:     org.ManagedBy,
			AmountCents:    amount,
			IdempotencyKey: idempotencyKey,
			Kind:           db.WalletEntryRefund,
			ProcessID:      vp.ID,
		}); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return "", false
		}
		return idempotencyKey, true
	}
	if a.paymentGW == nil {
		errors.ErrPaymentSessionConflict.
			Withf("the payment gateway is unavailable; the payment cannot be refunded").Write(w)
		return "", false
	}
	var firstAmount int64 // zero refunds the whole intent
	if withheldCents > 0 {
		if payment.ChargeSubtotalCents <= 0 {
			errors.ErrGenericInternalServerError.
				Withf("process %s has no recorded charge to withhold branding from", vp.ID.Hex()).Write(w)
			return "", false
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
			errors.ErrStripeError.Withf("could not refund the process payment").WithErr(err).Write(w)
			return "", false
		}
		if refundID == "" {
			refundID = refund.ID
		}
	}
	return refundID, true
}
