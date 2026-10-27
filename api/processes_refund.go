package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// refundPaidProcess returns a paid draft's money the way it arrived and records that it did:
// the payment row survives as the audit trail of a process that no longer exists, which is
// why the caller must not delete it. The once-per-organization add-ons it charged (branding,
// custom URL) are released with the refund, so the organization's next draft is quoted — and
// charged — for what the deleted one paid. Except each add-on another process already rides on:
// it is withheld from the refund and stays the organization's, since the add-on it bought is in
// use.
//
// What to withhold is fixed on the payment before any money moves (PlanProcessPaymentRefund),
// and a retry reuses it: recomputing it could flip once a sibling started or stopped relying
// on an add-on, and the retry would then refund under other keys than the money that
// already went back. The plan also locks the payment, so no census growth records mid-refund;
// a wallet-paid one is planned under the integrator's wallet lock too, because a wallet charge
// of census headroom debits before it records: planned between the two, the refund would leave
// that debit refused a record and returned by nobody.
//
// Reports false with the response already written when the money could not be returned.
// The draft stays then, delete-only, so the delete can be retried; destroying a draft whose
// money is still with us is the one outcome this must never produce.
//
// ponytail: a sibling's checkout that read the stamp before the release but stores its pending
// payment after the reliance check (a window as wide as the refund call) is priced without an
// add-on the organization no longer holds, and its payment pins that price. Closing it takes a
// re-check on both sides (checkout after storing, delete after releasing), if it shows up.
func (a *API) refundPaidProcess(w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment) bool {
	plan, ok := a.planPaidProcessRefund(w, vp, payment)
	if !ok {
		return false
	}
	refundID, err := a.returnProcessPayment(vp, payment, plan)
	if err != nil {
		if a.dropRefusedRefundPlan(vp, payment, plan, err) {
			errors.ErrPaymentSessionConflict.Withf("the payment cannot be refunded (e.g. it is disputed); " +
				"the process stays a paid draft: publish it, or contact support").Write(w)
			return false
		}
		writeSubscriptionError(w, err)
		return false
	}
	// released before the payment is marked refunded: a failure then leaves the delete to retry
	// (the refund is reused, the release is idempotent) instead of an organization keeping an
	// add-on it got its money back for
	if err := a.releaseOrgAddOnsNotWithheld(vp, payment, plan.withheld.AddOns); err != nil {
		errors.ErrGenericInternalServerError.
			Withf("the payment was refunded but its add-ons could not be released; retry the delete").
			WithErr(err).Write(w)
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
	log.Infow("paid draft refunded", "processId", vp.ID.Hex(), "amountCents", payment.AmountCents,
		"refundId", refundID, "wallet", payment.CheckoutSessionID == "", "withheldAddOns", plan.withheld.AddOns)
	return true
}

// processRefundPlan is what a delete's refund does with a paid process's money.
type processRefundPlan struct {
	withheld   db.RefundWithholding // net kept back: the relied-on add-ons, or nothing
	integrator common.Address       // the wallet a wallet-paid refund credits; zero for a card payment
}

// reliedOnOrgAddOns is what giving vp's payment back must withhold: each once-per-organization
// add-on the payment charged that another process of the organization relies on, at its list
// price. A delete's refund and a failed managed publish's credit-back both withhold it.
func (a *API) reliedOnOrgAddOns(vp *db.VotingProcess, payment *db.ProcessPayment) (db.RefundWithholding, error) {
	var withheld db.RefundWithholding
	for _, addOn := range db.OrgAddOns {
		if !payment.Charged(addOn) {
			continue
		}
		reliedOn, err := a.db.OrganizationAddOnReliedOn(vp.OrgAddress, addOn, vp.ID)
		if err != nil {
			return db.RefundWithholding{}, fmt.Errorf("failed to check %s reliance: %w", addOn, err)
		}
		if reliedOn {
			withheld.Cents += orgAddOnCents(addOn)
			withheld.AddOns = append(withheld.AddOns, addOn)
		}
	}
	return withheld, nil
}

// releaseOrgAddOnsNotWithheld gives back to the organization every once-per-organization add-on
// payment charged except the withheld ones, so its next process is charged for them again.
// Idempotent: each release is pinned to vp holding the claim.
func (a *API) releaseOrgAddOnsNotWithheld(
	vp *db.VotingProcess, payment *db.ProcessPayment, withheld []db.OrgAddOn,
) error {
	for _, addOn := range db.OrgAddOns {
		if !payment.Charged(addOn) || slices.Contains(withheld, addOn) {
			continue
		}
		if err := a.db.ReleaseOrganizationAddOn(vp.OrgAddress, addOn, vp.ID); err != nil {
			return fmt.Errorf("failed to release %s: %w", addOn, err)
		}
	}
	return nil
}

// planPaidProcessRefund returns the refund's plan: the one an earlier attempt fixed, or a new
// one — withholding the add-ons another process relies on, else nothing — fixed on the payment
// before the caller moves money. Reports false with the response written.
func (a *API) planPaidProcessRefund(
	w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment,
) (processRefundPlan, bool) {
	var plan processRefundPlan
	if payment.CheckoutSessionID == "" {
		org, err := a.db.Organization(vp.OrgAddress)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return plan, false
		}
		plan.integrator = org.ManagedBy
	}
	if payment.RefundWithheldCents != nil {
		plan.withheld = db.RefundWithholding{Cents: *payment.RefundWithheldCents, AddOns: payment.RefundWithheldAddOns}
		if plan.withheld.Cents > 0 && len(plan.withheld.AddOns) == 0 {
			// a plan fixed before the custom URL was once per organization withheld branding only
			plan.withheld.AddOns = []db.OrgAddOn{db.OrgAddOnBranding}
		}
		return plan, true
	}
	if plan.integrator != (common.Address{}) {
		// a headroom charge holds this lock from reading the payment to recording it, so its
		// debit lands before the plan (whose amount check then misses: retry the delete) or not
		// at all (it reads the plan and refuses)
		walletLock := a.walletLocks.lock(plan.integrator)
		defer walletLock.Unlock()
	}
	withheld, err := a.reliedOnOrgAddOns(vp, payment)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return plan, false
	}
	plan.withheld = withheld
	planned, err := a.db.PlanProcessPaymentRefund(vp.ID, payment.AmountCents, plan.withheld)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return plan, false
	}
	if !planned {
		// nothing has moved: a census top-up raised the payment after it was read
		errors.ErrPaymentSessionConflict.
			Withf("the process payment changed while it was being refunded; retry the delete").Write(w)
		return plan, false
	}
	return plan, true
}

// dropRefusedRefundPlan drops the plan of a refund Stripe refused for good (a disputed charge, an
// amount the intent no longer holds), when that provably moved nothing: a card payment with no
// census top-ups has one intent, and a refund of it this process already holds would have been
// returned as success, not refused. The draft is then a paid draft again, publishable, instead
// of stranded behind a plan no retry can carry out. Anything else keeps the plan — another
// intent may already be refunded — and is left to a human. Reports whether it dropped it.
func (a *API) dropRefusedRefundPlan(
	vp *db.VotingProcess, payment *db.ProcessPayment, plan processRefundPlan, err error,
) bool {
	if !errors.Is(err, stripe.ErrRefundRefused) {
		return false
	}
	if payment.CheckoutSessionID == "" || len(payment.TopUpIntents) > 0 {
		log.Errorw(err, fmt.Sprintf("refund of process %s refused for good after part of it may have moved;"+
			" needs manual reconciliation", vp.ID.Hex()))
		return false
	}
	dropped, dropErr := a.db.DropProcessPaymentRefundPlan(vp.ID, payment.AmountCents, plan.withheld.Cents)
	if dropErr != nil || !dropped {
		log.Errorw(fmt.Errorf("refund refused (%w), plan not dropped: %v", err, dropErr),
			fmt.Sprintf("process %s stays delete-only; needs manual reconciliation", vp.ID.Hex()))
		return false
	}
	log.Errorw(err, fmt.Sprintf("refund of process %s refused for good; it is a paid draft again", vp.ID.Hex()))
	return true
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
// plan.withheld.Cents is a net amount kept back (the relied-on add-ons): taken from a
// wallet credit as is, and from a card refund grossed up by the first charge's own VAT ratio.
//
// Nothing is written to the response: an error is an errors.Error carrying its HTTP status,
// or a plain one meaning 500 — write it with writeSubscriptionError.
func (a *API) returnProcessPayment(
	vp *db.VotingProcess, payment *db.ProcessPayment, plan processRefundPlan,
) (string, error) {
	withheldCents := plan.withheld.Cents
	idempotencyKey := "refund:" + vp.ID.Hex()
	if payment.CheckoutSessionID == "" {
		// paid from the integrator wallet: the money goes back where it came from, so the
		// integrator that funded the draft can spend it on the next one
		if plan.integrator == (common.Address{}) {
			return "", fmt.Errorf("process %s was paid from a wallet but its organization has no integrator", vp.ID.Hex())
		}
		amount := payment.AmountCents - withheldCents
		if amount <= 0 {
			return idempotencyKey, nil // the withheld add-ons were all it paid for, and they stay
		}
		if err := a.db.CreditWallet(db.WalletCredit{
			OrgAddress:     plan.integrator,
			AmountCents:    amount,
			IdempotencyKey: idempotencyKey,
			Kind:           db.WalletEntryCreditBack,
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
	var firstAmount pricing.Cents // zero refunds the whole intent
	if withheldCents > 0 {
		if payment.ChargeSubtotalCents <= 0 {
			return "", fmt.Errorf("process %s has no recorded charge to withhold add-ons from", vp.ID.Hex())
		}
		// the withheld net grossed up by the first charge's VAT ratio, rounded half up
		withheld := (withheldCents*payment.ChargeTotalCents + payment.ChargeSubtotalCents/2) /
			payment.ChargeSubtotalCents
		firstAmount = payment.ChargeTotalCents - withheld
	}
	var refundID string
	for i, intent := range append([]string{payment.PaymentIntentID}, payment.TopUpIntents...) {
		amount := pricing.Cents(0) // top-ups never carry add-ons, so they come back whole
		if i == 0 && withheldCents > 0 {
			if firstAmount <= 0 {
				refundID = "withheld:" + intent // the withheld add-ons were all it paid for, and they stay
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
