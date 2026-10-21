package api

import (
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// refundPaidProcess credits a paid draft's money back to the wallet that paid it
// (payment.Payer) and records that it did: the payment row survives as the audit trail of a
// process that no longer exists, which is why the caller must not delete it. Money never goes
// back to a card. The once-per-organization add-ons it charged (branding, custom URL) are
// released with the credit, so the organization's next draft is quoted — and charged — for what
// the deleted one paid. Except each add-on another process already rides on: it is withheld from
// the credit and stays the organization's, since the add-on it bought is in use.
//
// What to withhold is fixed on the payment before any money moves (PlanProcessPaymentRefund),
// and a retry reuses it: recomputing it could flip once a sibling started or stopped relying
// on an add-on. The plan also locks the payment, so no census growth records mid-refund; it is
// planned under the payer's wallet lock too, because a wallet charge of census headroom debits
// before it records: planned between the two, the credit would leave that debit refused a record
// and returned by nobody.
//
// Returns why the money could not be credited, as an errors.Error or a plain error meaning 500.
// The draft stays then, delete-only, so the delete can be retried.
func (a *API) refundPaidProcess(vp *db.VotingProcess, payment *db.ProcessPayment) error {
	plan, err := a.planPaidProcessRefund(vp, payment)
	if err != nil {
		return err
	}
	if err := a.returnProcessPayment(vp, payment, plan); err != nil {
		return err
	}
	// released before the payment is marked refunded: a failure then leaves the delete to retry
	// (the credit is keyed, the release is idempotent) instead of an organization keeping an
	// add-on it got its money back for
	if err := a.releaseOrgAddOnsNotWithheld(vp, payment, plan.AddOns); err != nil {
		return errors.ErrGenericInternalServerError.
			Withf("the payment was credited back but its add-ons could not be released; retry the delete").
			WithErr(err)
	}
	marked, err := a.db.MarkProcessPaymentRefunded(vp.ID, payment.AmountCents)
	if err != nil {
		// the money is already back; losing the status write would let a retry look unpaid,
		// so this is reported rather than swallowed
		return fmt.Errorf("failed to mark process payment refunded: %w", err)
	}
	if !marked {
		// the plan locks the envelope, so this is only a guard: a payment that moved anyway
		// is not recorded as refunded, and the draft stays for a retry
		return errors.ErrPaymentSessionConflict.
			Withf("the process payment changed while it was being refunded; retry the delete")
	}
	log.Infow("paid draft credited back", "processId", vp.ID.Hex(), "amountCents", payment.AmountCents,
		"payer", payment.Payer.String(), "withheldAddOns", plan.AddOns)
	return nil
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

// planPaidProcessRefund returns the refund's withholding: the one an earlier attempt fixed, or a
// new one — withholding the add-ons another process relies on, else nothing — fixed on the
// payment before the caller moves money.
func (a *API) planPaidProcessRefund(vp *db.VotingProcess, payment *db.ProcessPayment) (db.RefundWithholding, error) {
	if payment.RefundWithheldCents != nil {
		withheld := db.RefundWithholding{Cents: *payment.RefundWithheldCents, AddOns: payment.RefundWithheldAddOns}
		return withheld, nil
	}
	if payment.Payer != (common.Address{}) {
		// a headroom charge holds this lock from reading the payment to recording it, so its
		// debit lands before the plan (whose amount check then misses: retry the delete) or not
		// at all (it reads the plan and refuses)
		walletLock := a.walletLocks.lock(payment.Payer)
		defer walletLock.Unlock()
	}
	withheld, err := a.reliedOnOrgAddOns(vp, payment)
	if err != nil {
		return withheld, err
	}
	planned, err := a.db.PlanProcessPaymentRefund(vp.ID, payment.AmountCents, withheld)
	if err != nil {
		return withheld, fmt.Errorf("failed to plan the refund: %w", err)
	}
	if !planned {
		// nothing has moved: a census top-up raised the payment after it was read
		return withheld, errors.ErrPaymentSessionConflict.
			Withf("the process payment changed while it was being refunded; retry the delete")
	}
	return withheld, nil
}

// refuseRefundedDraft refuses a draft a delete started refunding and did not finish dropping:
// its money is back, or its refund is fixed and on its way, so finishing that delete is all it
// can do. Publishing it again would be free, since the wallet debit is keyed per (process,
// attempt, price) and the original key stays applied after its refund; growing it would buy voters its
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

// returnProcessPayment credits the payment back to the wallet that paid it (payment.Payer),
// less withheld, keyed "refund:<process>" so a retried delete cannot credit twice. The credit
// covers AmountCents, which already includes census headroom; the refund plan keeps it from
// growing further, so one key per process is enough. A €0 envelope, or one the withheld add-ons
// cover entirely, credits nothing.
//
// Nothing is written to the response: an error is an errors.Error carrying its HTTP status,
// or a plain one meaning 500 — write it with writeSubscriptionError.
func (a *API) returnProcessPayment(
	vp *db.VotingProcess, payment *db.ProcessPayment, withheld db.RefundWithholding,
) error {
	amount := payment.AmountCents - withheld.Cents
	if amount <= 0 {
		return nil // the withheld add-ons were all it paid for, and they stay
	}
	if payment.Payer == (common.Address{}) {
		return fmt.Errorf("process %s paid %d eur cents but records no payer", vp.ID.Hex(), payment.AmountCents)
	}
	// a managed organization's credit-back frees room under its spend cap; a payer paying for
	// its own process has no cap
	var spentBy common.Address
	if payment.Payer != vp.OrgAddress {
		spentBy = vp.OrgAddress
	}
	if err := a.db.CreditWallet(db.WalletCredit{
		OrgAddress:        payment.Payer,
		AmountCents:       amount,
		IdempotencyKey:    "refund:" + vp.ID.Hex(),
		Kind:              db.WalletEntryCreditBack,
		ProcessID:         vp.ID,
		ProcessOrgAddress: vp.OrgAddress,
		SpentBy:           spentBy,
	}); err != nil {
		return fmt.Errorf("failed to credit the payment back to the wallet: %w", err)
	}
	return nil
}
