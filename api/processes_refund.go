package api

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
)

// errRefundedDraft refuses a draft a delete credited back and did not finish dropping: its money
// is back, so finishing that delete is all it can do. Publishing it again would be free, since the
// wallet debit is keyed per (process, attempt, price) and the original key stays applied after
// its refund; growing it would buy voters its refund does not return.
var errRefundedDraft = errors.ErrPaymentSessionConflict.
	Withf("the process is refunded by a delete that did not finish; delete it again")

// refundPaidProcess credits what a draft paid back to the wallet that paid it, before the draft
// goes. Money never goes back to a card. The once-per-organization add-ons it charged (branding,
// custom URL) are released with the credit, so the organization's next draft is quoted — and
// charged — for what the deleted one paid. Except each add-on another process already rides on: it
// is withheld from the credit and stays the organization's, since the add-on it bought is in use.
//
// It runs in one hold of the payer's wallet lock, which every charge of the payer takes too: what
// the process paid is read off the ledger, the withholding decided, and the credit-back made under
// the process's refund key (db.RefundCreditKey), its row carrying what it withheld and the add-ons
// it releases. Every payment is settled by that row, even one its withheld add-ons account for
// entirely (a credit of 0). A retry after any failure reads the ledger again: credited, the
// process paid nothing more and only the owed release is left to apply (healOwedRelease).
//
// In an organization teardown (non-nil teardown) the add-on is withheld only when a process
// actually received it, published or with elections mined, as the teardown recorded before it
// dropped anything: the other drafts relying on the stamp go in the same sweep, so withholding for
// them would strand the price with the deleted organization.
//
// Returns why the money could not be credited, as an errors.Error or a plain error meaning 500.
// The draft stays then, delete-only once credited, so the delete can be retried.
func (a *API) refundPaidProcess(vp *db.VotingProcess, teardown *orgTeardown) error {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	payer := walletPayer(org)
	// held from the withholding decision through the add-on release: a sibling's publish
	// prices and debits under this lock too, so it either relies on the add-on before the
	// decision (withheld) or prices it after the release (it pays) — never free in between
	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
	if err != nil {
		return err // nothing moved yet: the draft stays and the delete is retried
	}
	defer walletLock.Unlock()
	paid, err := a.processPaidState(vp.ID, payer)
	if err != nil {
		return err
	}
	if paidCents := paid.PaidCents; paidCents > 0 {
		withheld, err := a.reliedOnOrgAddOns(vp, paid.ProcessLedgerState, teardown)
		if err != nil {
			return err
		}
		amount := max(paidCents-withheld.Cents, 0)
		// the row settles the whole envelope: what it returns and what it withholds
		withheld.Cents = paidCents - amount
		if err := a.creditProcessBack(processCreditBack{
			vp: vp, payer: payer, key: db.RefundCreditKey(vp.ID),
			amountCents: amount, withheld: withheld, addOns: paid.AddOns,
		}); err != nil {
			return err
		}
		log.Infow("paid draft credited back", "processId", vp.ID.Hex(), "amountCents", amount,
			"payer", payer.String(), "withheldAddOns", withheld.AddOns)
		if paid, err = a.processPaidState(vp.ID, payer); err != nil {
			return err
		}
		if paid.PaidCents > 0 {
			// a debit landed between the read and the credit: only possible from another instance
			// (the wallet lock is in-process), and the refund key cannot credit it back
			log.Errorw(fmt.Errorf("process %s still nets %d eur cents after its refund", vp.ID.Hex(), paid.PaidCents),
				"a debit raced the draft's refund")
		}
		a.refreshPaymentCache(vp, db.ProcessPaymentRefunded, paidCents)
	}
	// released after the credit, so a failure leaves the delete to retry instead of an
	// organization keeping an add-on it got its money back for
	if err := a.healOwedRelease(vp, paid); err != nil {
		return errors.ErrGenericInternalServerError.
			Withf("the payment was credited back but its add-ons could not be released; retry the delete").
			WithErr(err)
	}
	return nil
}

// processCreditBack is one credit of what a process paid back to its payer: under key, amountCents
// (0 when withheld covers it all), withheld kept back, and every other add-on of addOns released.
type processCreditBack struct {
	vp          *db.VotingProcess
	payer       common.Address
	key         string
	amountCents pricing.Cents
	withheld    db.RefundWithholding
	addOns      []db.OrgAddOn
}

// creditProcessBack makes the credit-back c describes, its ledger row recording what it withheld
// and the add-ons it gives back (released by healOwedRelease once the caller reads the ledger
// again). A managed organization's credit-back frees room under its spend cap; a payer paying for
// its own process has no cap.
func (a *API) creditProcessBack(c processCreditBack) error {
	var release []db.OrgAddOn
	for _, addOn := range c.addOns {
		if !slices.Contains(c.withheld.AddOns, addOn) {
			release = append(release, addOn)
		}
	}
	// a withheld add-on stays the organization's: stamp it first (first-wins, a no-op when it is
	// there), or a stamp its charge never wrote would leave it priced, and paid again, by the very
	// sibling it was withheld for. Before the row, so the stamp predates the withholding, as the
	// teardown's credit of an undelivered add-on requires (paidAt <= withheldAt).
	for _, addOn := range c.withheld.AddOns {
		if _, err := a.db.SetOrganizationAddOnPaid(c.vp.OrgAddress, addOn, time.Now()); err != nil {
			return fmt.Errorf("failed to stamp the withheld %s add-on: %w", addOn, err)
		}
	}
	var spentBy common.Address
	if c.payer != c.vp.OrgAddress {
		spentBy = c.vp.OrgAddress
	}
	if err := a.db.CreditWallet(db.WalletCredit{
		OrgAddress:        c.payer,
		AmountCents:       c.amountCents,
		IdempotencyKey:    c.key,
		Kind:              db.WalletEntryCreditBack,
		ProcessID:         c.vp.ID,
		ProcessOrgAddress: c.vp.OrgAddress,
		SpentBy:           spentBy,
		Withheld:          c.withheld,
		ReleaseAddOns:     release,
	}); err != nil {
		return fmt.Errorf("failed to credit the payment back to the wallet: %w", err)
	}
	return nil
}

// healOwedRelease applies the add-on release the process's last credit-back owes (the add-ons it
// charged and gave back), while no payment has followed it. Each release is pinned to the process
// as claimant and to a claim taken before that credit-back, so applying it again — or late — never
// touches a claim the process took since; it is idempotent, and a failure leaves it owed.
func (a *API) healOwedRelease(vp *db.VotingProcess, paid processPaid) error {
	if len(paid.OwedRelease) == 0 {
		return nil
	}
	if err := atMoneyStep(moneyStepRelease, vp.ID); err != nil {
		return err
	}
	for _, addOn := range paid.OwedRelease {
		if err := a.db.ReleaseOrganizationAddOn(db.OrgAddOnClaim{
			OrgAddress: vp.OrgAddress, AddOn: addOn, ProcessID: vp.ID, ClaimedBefore: paid.OwedSince,
		}); err != nil {
			return fmt.Errorf("failed to release %s: %w", addOn, err)
		}
	}
	return nil
}

// reliedOnOrgAddOns is what giving vp's payment back must withhold: each once-per-organization
// add-on its payment charged (paid, its ledger state read from its payer's wallet) that another
// process of the organization relies on, at its list price. A delete's refund and a failed
// publish's credit-back both withhold it. In an organization teardown (non-nil teardown) only an
// add-on the teardown found delivered is relied on, so the result does not depend on the order
// drafts go in.
func (a *API) reliedOnOrgAddOns(
	vp *db.VotingProcess, paid *db.ProcessLedgerState, teardown *orgTeardown,
) (db.RefundWithholding, error) {
	var withheld db.RefundWithholding
	for _, addOn := range paid.AddOns {
		reliedOn := teardown != nil && slices.Contains(teardown.delivered, addOn)
		if teardown == nil {
			var err error
			if reliedOn, err = a.db.OrganizationAddOnReliedOn(db.OrganizationAddOnReliedOnParams{
				Payer: paid.Payer, OrgAddress: vp.OrgAddress, AddOn: addOn, ExceptProcessID: vp.ID,
			}); err != nil {
				return db.RefundWithholding{}, fmt.Errorf("failed to check %s reliance: %w", addOn, err)
			}
		}
		if reliedOn {
			withheld.Cents += orgAddOnCents(addOn)
			withheld.AddOns = append(withheld.AddOns, addOn)
		}
	}
	return withheld, nil
}

// refuseRefundedDraft refuses (errRefundedDraft) a draft a delete credited back and did not finish
// dropping, as its ledger shows it. A state it cannot read is returned, never taken as absent.
func (a *API) refuseRefundedDraft(vp *db.VotingProcess) error {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	state, err := a.db.ProcessLedgerState(walletPayer(org), vp.ID)
	if err != nil {
		return fmt.Errorf("failed to read the process ledger: %w", err)
	}
	if state.Refunded {
		return errRefundedDraft
	}
	return nil
}
