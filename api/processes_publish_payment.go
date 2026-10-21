package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// walletPayer is the wallet an organization's processes are paid from: its integrator's when it
// is managed, its own otherwise.
func walletPayer(org *db.Organization) common.Address {
	if org.ManagedBy != (common.Address{}) {
		return org.ManagedBy
	}
	return org.Address
}

// walletShortError is the 402 of a payer whose balance does not cover dueCents of quote: 40175
// with requiredCents/availableCents for a managed organization (its integrator pays), 40178 with
// the quote, the balance and the shortfall a top-up checkout covers for any other.
func walletShortError(org *db.Organization, quote pricing.Quote, dueCents, balanceCents pricing.Cents) error {
	if org.ManagedBy != (common.Address{}) {
		return errors.ErrInsufficientWalletBalance.WithData(map[string]pricing.Cents{
			"requiredCents":  dueCents,
			"availableCents": balanceCents,
		})
	}
	return errors.ErrPaymentRequired.WithData(&apicommon.ProcessPaymentRequired{
		Quote:          quote,
		BalanceCents:   balanceCents,
		ShortfallCents: dueCents - balanceCents,
	})
}

// paymentDueForPublish is the publish gate: it refuses, before the publish is claimed, what the
// debit inside startProcessPublish would refuse anyway, with the answer the client needs. Nil
// means clear to publish: the process is free, already paid, or its payer's wallet covers what is
// due. Otherwise: ErrQuoteRequired (422) for a quote-only census, 409 while a top-up checkout opened
// for the process is still open, and walletShortError (402) when the wallet falls short. Only a
// courtesy: the balance can move before the debit, whose own refusal is the authoritative answer.
func (a *API) paymentDueForPublish(vp *db.VotingProcess) error {
	if err := a.refuseRefundedDraft(vp.ID); err != nil {
		return err
	}
	quote, _, err := a.processQuote(vp)
	if err != nil {
		return err
	}
	// quote-only: there is no price, and its zero total must never read as free
	if quote.QuoteRequired {
		return errors.ErrQuoteRequired
	}
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	var paidCents pricing.Cents
	if err == nil {
		switch {
		case payment.Status == db.ProcessPaymentPending && !payment.Consumed:
			// publishing over it would leave a payable session behind (even for a draft that
			// shrank to free): the customer pays it, or cancels it first
			return errors.ErrPaymentSessionConflict.Withf("a wallet top-up checkout is open for the process; " +
				"pay it, or cancel it with DELETE /processes/{processId}/checkout, and publish again")
		case payment.Status == db.ProcessPaymentPaid:
			paidCents = payment.AmountCents
		default:
		}
	}
	dueCents := quote.TotalCents - paidCents
	if dueCents <= 0 {
		return nil // free, or covered by what was paid
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	wallet, err := a.db.Wallet(walletPayer(org))
	if err != nil {
		return fmt.Errorf("failed to get wallet: %w", err)
	}
	if wallet.BalanceCents >= dueCents {
		return nil
	}
	return walletShortError(org, quote, dueCents, wallet.BalanceCents)
}

// debitProcessWallet charges a draft's publish to its payer's wallet: atomic, and priced against
// the draft as it is right now. It is the only debit of a publish, and runs inside
// startProcessPublish after the publish claim, so two concurrent publishes cannot both debit and
// a delete cannot refund between the debit and the publish. A publish retry re-prices, so a retry
// of an unchanged draft debits nothing while one whose census grew between the attempts pays the
// difference. A draft whose earlier publish was credited back pays again, in full. Refused
// without touching the balance when the wallet does not cover it.
func (a *API) debitProcessWallet(vp *db.VotingProcess, org *db.Organization) error {
	// a credit-back an earlier attempt owes lands before this attempt pays again
	if payment, err := a.db.ProcessPayment(vp.ID); err == nil {
		if err := a.ensurePublishCreditBack(payment); err != nil {
			return err
		}
	} else if !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		return err
	}
	// win the add-on claims before the debit: two publishes racing here would otherwise both
	// price a once-per-organization add-on in and both be charged for it
	quote, claims, err := a.claimOrgAddOnsForPayment(vp, org, &input)
	if err != nil {
		a.releaseRefusedOrgAddOnClaims(vp, claims)
		return err
	}
	// a quote-only total is not free: chargeProcessWallet refuses it
	if quote.TotalCents == 0 && !quote.QuoteRequired {
		return nil
	}
	_, err = a.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: quote, branding: input.Branding, customURL: input.CustomURL,
	})
	// refused before the debit, nothing backs the claims; any later failure keeps them, since
	// the debit it follows may already be applied
	if errors.Is(err, errors.ErrInsufficientWalletBalance) || errors.Is(err, errors.ErrPaymentRequired) ||
		errors.Is(err, errors.ErrQuoteRequired) || errors.Is(err, errors.ErrManagedOrgSpendCapReached) {
		a.releaseRefusedOrgAddOnClaims(vp, claims)
	}
	return err
}

// processCharge is one charge of a process against its payer's wallet: quote is the full price
// the process must end up having paid, and branding and customURL whether quote prices each
// once-per-organization add-on in. fromCents, when set, makes the charge apply only while the
// process has paid exactly that (census headroom bought through a checkout priced from it).
type processCharge struct {
	vp        *db.VotingProcess
	org       *db.Organization
	quote     pricing.Quote
	branding  bool
	customURL bool
	fromCents *pricing.Cents
}

// chargeProcessWallet debits the payer's wallet for whatever of c.quote is not covered yet and
// records the new price, so both the publish path and a census that outgrew its price charge the
// same way: only the delta, at most once per (process, price). Returns walletShortError when the
// balance does not cover it, or ErrQuoteRequired when the price needs a custom quote, without
// touching the wallet. It returns what it debited: 0 when the price was already covered.
//
// Charges to one wallet run one at a time, and what the process paid already is read inside
// that lock: two headroom purchases racing at different prices would otherwise both charge
// the delta from the same base, and the second would pay for voters the first already bought.
//
// The debit and the paid record cannot be one write, so the record is not optional: a debit
// whose record is lost would leave the process looking free, and its next growth or refund
// would be computed from nothing. A payment the wallet may not overwrite is refused before any
// money moves; a record write that still fails is an error the caller must stop on, and a retry
// heals it, since the debit key is already applied and charges nothing.
func (a *API) chargeProcessWallet(c processCharge) (pricing.Cents, error) {
	// above the self-service limit there is no price, only a custom quote, never a wallet debit.
	// Checked first: a quote-only total of 0 would read as "already covered" by any payment.
	if c.quote.QuoteRequired {
		return 0, errors.ErrQuoteRequired
	}
	payer := walletPayer(c.org)
	// ponytail: in-memory lock, like orgTxLocks — one API instance; a DB CAS if that changes
	walletLock := a.walletLocks.lock(payer)
	defer walletLock.Unlock()
	// A payment state we cannot read is never assumed absent: that would debit the whole
	// price a second time.
	paid, err := a.db.ProcessPayment(c.vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return 0, fmt.Errorf("failed to get process payment: %w", err)
	}
	if err == nil && !walletMayRecord(paid) {
		return 0, errors.ErrPaymentSessionConflict.Withf(
			"process %s has a %s payment the wallet cannot pay for", c.vp.ID.Hex(), paid.Status)
	}
	// the paid record written below replaces a reverted payment and what it still owes: apply
	// that credit first (a census headroom purchase can race a failed publish's reversal)
	if err == nil {
		if err := a.ensurePublishCreditBack(paid); err != nil {
			return 0, err
		}
	}
	var alreadyCents pricing.Cents
	if err == nil && paid.Status == db.ProcessPaymentPaid {
		alreadyCents = paid.AmountCents
	}
	if c.fromCents != nil && alreadyCents != *c.fromCents {
		return 0, errors.ErrPaymentSessionConflict.Withf("process %s paid %d eur cents, not the %d this charge was priced from",
			c.vp.ID.Hex(), alreadyCents, *c.fromCents)
	}
	if alreadyCents >= c.quote.TotalCents {
		return 0, nil // this price is already covered
	}
	dueCents := c.quote.TotalCents - alreadyCents
	// the attempt survives every state the wallet may record over: a credit-back moved it on,
	// so this debit is not mistaken for one an earlier, credited-back attempt already applied
	var attempt int
	if paid != nil {
		attempt = paid.WalletAttempt
	}
	// only a managed organization's debits count towards a spend cap (its integrator's)
	var managedOrg common.Address
	if c.org.ManagedBy != (common.Address{}) {
		managedOrg = c.vp.OrgAddress
	}
	if err := a.db.DebitWalletForProcess(db.WalletDebit{
		OrgAddress:        payer,
		ProcessID:         c.vp.ID,
		ProcessOrgAddress: c.vp.OrgAddress,
		AmountCents:       dueCents,
		PriceCents:        c.quote.TotalCents,
		Attempt:           attempt,
		ManagedOrg:        managedOrg,
	}); err != nil {
		if errors.Is(err, db.ErrManagedOrgSpendCapReached) {
			wallet, werr := a.db.Wallet(payer)
			if werr != nil || wallet.ManagedOrgSpend(c.vp.OrgAddress).CapCents == nil {
				return 0, errors.ErrManagedOrgSpendCapReached
			}
			spend := wallet.ManagedOrgSpend(c.vp.OrgAddress)
			return 0, errors.ErrManagedOrgSpendCapReached.WithData(map[string]pricing.Cents{
				"capCents":      *spend.CapCents,
				"spentCents":    spend.SpentCents,
				"requiredCents": dueCents,
			})
		}
		if errors.Is(err, db.ErrInsufficientWalletBalance) {
			wallet, werr := a.db.Wallet(payer)
			if werr != nil {
				return 0, fmt.Errorf("wallet short, and its balance could not be read: %w", werr)
			}
			return 0, walletShortError(c.org, c.quote, dueCents, wallet.BalanceCents)
		}
		return 0, fmt.Errorf("failed to debit wallet: %w", err)
	}
	walletPayment := &db.ProcessPayment{
		ProcessID:     c.vp.ID,
		OrgAddress:    c.vp.OrgAddress,
		Payer:         payer,
		AmountCents:   c.quote.TotalCents,
		Currency:      "eur",
		Branding:      c.branding,
		CustomURL:     c.customURL,
		WalletAttempt: attempt,
	}
	if paid != nil {
		// keep the record's history across a top-up, and the checkout that funded it
		walletPayment.CreatedAt = paid.CreatedAt
		walletPayment.PaidAt = paid.PaidAt
		walletPayment.RequestedBy = paid.RequestedBy
		walletPayment.CheckoutSessionID = paid.CheckoutSessionID
	}
	// ponytail: a retry after a lost record is priced from zero again, which only matters if the
	// census also grew in between (then it debits the whole new price); resolving the prior
	// debit from the wallet ledger closes that if it ever shows up
	recorded, err := a.db.SetProcessPaymentPaidByWallet(walletPayment)
	if err != nil {
		return 0, fmt.Errorf("wallet debited but its process payment was not recorded: %w", err)
	}
	if !recorded {
		return 0, errors.ErrPaymentSessionConflict.Withf(
			"the payment of process %s changed while the wallet paid for it; retry", c.vp.ID.Hex())
	}
	// stamp each once-per-organization add-on the debited quote carried as paid, so the org's
	// next process is not charged it again (conditional in the DB: only the first payment sets it)
	for _, addOn := range db.OrgAddOns {
		if !walletPayment.Charged(addOn) {
			continue
		}
		if _, err := a.db.SetOrganizationAddOnPaid(c.vp.OrgAddress, addOn, time.Now()); err != nil {
			log.Warnw("could not stamp organization add-on paid", "processId", c.vp.ID.Hex(),
				"orgAddress", c.vp.OrgAddress.String(), "addOn", addOn, "error", err)
		}
	}
	return dueCents, nil
}

// reversePublishDebit gives a draft's wallet payment back to its payer when its publish failed
// for good before any election was mined: the publish worker's abandon, the publish request
// failing after the debit, and the stale-marker sweep after a restart. The caller holds the
// publish claim. A draft with mined elections keeps the debit — they exist and were paid for, and
// publishing again resumes them for free.
//
// The payment is reverted first (RevertWalletProcessPayment: failed, WalletAttempt moved on, the
// credit it owes recorded), then credited (ensurePublishCreditBack). In that order a failure in
// between leaves the payer owed rather than a draft both credited and still paid — which the next
// publish would take as paid for free — and the owed credit is applied by whatever touches the
// payment next: another reversal, the next publish's debit, a checkout, or the draft's delete.
// Once-per-organization add-ons another process already relies on are withheld and stay the
// organization's, as a delete's credit-back withholds them; the others are released with the money.
//
// Anything but an unrefunded wallet payment (none, an open checkout, a refund in progress) is
// left alone and reports nil.
func (a *API) reversePublishDebit(processID bson.ObjectID) error {
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		return fmt.Errorf("failed to get process: %w", err)
	}
	if vp.Published || anyMined(questions) {
		return nil
	}
	// the payment names the wallet to lock; it is read again under that lock
	payment, err := a.db.ProcessPayment(processID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	// a census headroom charge debits before it records; reading the payment under the same lock
	// means a reversal never misses a debit that charge has made
	walletLock := a.walletLocks.lock(payment.Payer)
	defer walletLock.Unlock()
	payment, err = a.db.ProcessPayment(processID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	if payment.Status == db.ProcessPaymentPaid && payment.RefundWithheldCents == nil && payment.AmountCents > 0 {
		withheld, err := a.reliedOnOrgAddOns(vp, payment)
		if err != nil {
			return err
		}
		owed := payment.AmountCents - withheld.Cents
		reverted, err := a.db.RevertWalletProcessPayment(db.WalletPaymentRevert{
			ProcessID: processID, AmountCents: payment.AmountCents, Attempt: payment.WalletAttempt, CreditBackCents: owed,
		})
		if err != nil {
			return fmt.Errorf("failed to revert the wallet payment: %w", err)
		}
		if !reverted {
			return fmt.Errorf("the payment of process %s moved while it was being reverted", processID.Hex())
		}
		if err := a.releaseOrgAddOnsNotWithheld(vp, payment, withheld.AddOns); err != nil {
			log.Errorw(err, fmt.Sprintf("add-ons of credited-back process %s not released", processID.Hex()))
		}
		payment.Status, payment.CreditBackCents = db.ProcessPaymentFailed, owed
		payment.WalletAttempt++
		log.Infow("failed publish credited back to the payer's wallet", "processId", processID.Hex(),
			"payer", payment.Payer.String(), "amountCents", owed, "attempt", payment.WalletAttempt-1)
	}
	return a.ensurePublishCreditBack(payment)
}

// ensurePublishCreditBack applies the credit a reverted wallet payment owes its payer's wallet
// (see reversePublishDebit), keyed by the attempt that paid, so applying it again is a no-op. Any
// other payment owes nothing and reports nil. Every path that is about to debit the draft again,
// replace its payment or drop it calls it first, so an owed credit is never lost to them.
func (a *API) ensurePublishCreditBack(payment *db.ProcessPayment) error {
	if payment.Status != db.ProcessPaymentFailed || payment.WalletAttempt == 0 || payment.CreditBackCents <= 0 {
		return nil
	}
	if payment.Payer == (common.Address{}) {
		return fmt.Errorf("process %s owes a wallet credit but records no payer", payment.ProcessID.Hex())
	}
	// a managed organization's credit-back frees room under its spend cap; a payer paying for
	// its own process has no cap
	var spentBy common.Address
	if payment.Payer != payment.OrgAddress {
		spentBy = payment.OrgAddress
	}
	if err := a.db.CreditWallet(db.WalletCredit{
		OrgAddress:        payment.Payer,
		AmountCents:       payment.CreditBackCents,
		IdempotencyKey:    fmt.Sprintf("publishfail:%s:%d", payment.ProcessID.Hex(), payment.WalletAttempt-1),
		Kind:              db.WalletEntryCreditBack,
		ProcessID:         payment.ProcessID,
		ProcessOrgAddress: payment.OrgAddress,
		SpentBy:           spentBy,
	}); err != nil {
		return fmt.Errorf("failed to credit the wallet back: %w", err)
	}
	return nil
}

// walletMayRecord reports whether SetProcessPaymentPaidByWallet may write over payment: a
// pending payment whose top-up was already credited (consumed), a reverted one, or a paid one no
// delete is refunding. An open checkout is not: the customer may still pay it.
func walletMayRecord(payment *db.ProcessPayment) bool {
	switch payment.Status {
	case db.ProcessPaymentPending:
		return payment.Consumed
	case db.ProcessPaymentFailed:
		return true
	case db.ProcessPaymentPaid:
		return payment.RefundWithheldCents == nil
	default:
		return false
	}
}

// applyProcessTopUp is the webhook's follow-up of a credited top-up opened for a process
// (stripe.Service.OnProcessTopUp). The credit already landed in the wallet and stays there
// whatever happens here: every refusal leaves it for a manual publish (or headroom purchase) and
// returns nil, and only a failure a retry can clear is returned, so the webhook answers 500 and
// Stripe redelivers.
//
// A publish top-up acts only for the pending payment of this very session: it consumes it (a
// compare-and-set on the session) and publishes, which debits inside startProcessPublish. A
// redelivery finds it consumed (a crash before the publish: it publishes now) or paid by this
// session (publishing again is free), and anything else — no payment, another session, refunded
// or reverted — stops it. So a Stripe retry never debits a draft whose payment was given back.
func (a *API) applyProcessTopUp(t stripe.ProcessTopUp) error {
	if t.HeadroomCents > 0 {
		return a.applyHeadroomTopUp(t)
	}
	payment, err := a.db.ProcessPayment(t.ProcessID)
	if errors.Is(err, db.ErrNotFound) {
		log.Infow("top-up credited for a process with no payment, kept in the wallet",
			"processId", t.ProcessID.Hex(), "sessionId", t.SessionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	if payment.CheckoutSessionID != t.SessionID ||
		(payment.Status != db.ProcessPaymentPending && payment.Status != db.ProcessPaymentPaid) {
		log.Infow("top-up credited for a process that no longer waits on it, kept in the wallet",
			"processId", t.ProcessID.Hex(), "sessionId", t.SessionID, "paymentStatus", payment.Status)
		return nil
	}
	if payment.Status == db.ProcessPaymentPending && !payment.Consumed {
		consumed, err := a.db.ConsumeProcessPaymentTopUp(t.ProcessID, t.SessionID)
		if err != nil {
			return fmt.Errorf("failed to consume the process top-up: %w", err)
		}
		if !consumed {
			// a concurrent delivery consumed it, or a cancel dropped it: publishPaidProcess
			// re-reads the payment and acts only on the first
			log.Infow("process top-up consumed concurrently", "processId", t.ProcessID.Hex(), "sessionId", t.SessionID)
		}
	}
	return a.publishPaidProcess(t)
}

// applyHeadroomTopUp debits census headroom a credited top-up paid for: the wallet pays the
// process's envelope up to t.HeadroomCents, only while the process still paid t.FromCents — the
// base the checkout was priced from. A replay finds the envelope raised and debits nothing; any
// refusal leaves the credit in the wallet, where POST /processes/{processId}/census/checkout
// spends it.
func (a *API) applyHeadroomTopUp(t stripe.ProcessTopUp) error {
	vp, err := a.db.VotingProcess(t.ProcessID)
	if errors.Is(err, db.ErrNotFound) {
		log.Infow("headroom top-up credited for a deleted process, kept in the wallet", "processId", t.ProcessID.Hex())
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get process: %w", err)
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	payment, err := a.db.ProcessPayment(t.ProcessID)
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	from := t.FromCents
	debited, err := a.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: t.HeadroomCents},
		branding: payment.Branding, customURL: payment.CustomURL, fromCents: &from,
	})
	var refusal errors.Error
	if errors.As(err, &refusal) {
		log.Infow("headroom top-up not applied, kept in the wallet", "processId", t.ProcessID.Hex(),
			"sessionId", t.SessionID, "reason", err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	log.Infow("census headroom paid from a top-up", "processId", t.ProcessID.Hex(), "sessionId", t.SessionID,
		"targetCents", t.HeadroomCents, "debitedCents", debited)
	return nil
}

// dropProcessCheckout is the webhook's handling of a top-up checkout that expired or whose
// delayed payment failed (stripe.Service.OnProcessCheckoutDropped): nothing was credited, so the
// pending payment holding the session goes, and with it the add-on claims it held. A newer
// checkout of the same draft is left alone, and so is a payment whose top-up was already credited
// (consumed): a late expiry or failure of that session, replayed or out of order, drops nothing.
func (a *API) dropProcessCheckout(processID bson.ObjectID, sessionID string) error {
	payment, err := a.db.ProcessPayment(processID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	if err == nil && payment.CheckoutSessionID == sessionID && payment.Consumed {
		log.Infow("checkout already credited; nothing to drop", "processId", processID.Hex(), "sessionId", sessionID)
		return nil
	}
	vp, err := a.db.VotingProcess(processID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("failed to get process: %w", err)
	}
	var org *db.Organization
	if vp != nil {
		// read before the payment is dropped, so the claims released below are its own
		if org, err = a.db.Organization(vp.OrgAddress); err != nil {
			return fmt.Errorf("failed to get organization: %w", err)
		}
	}
	dropped, err := a.db.DeleteProcessPaymentCheckout(processID, sessionID)
	if err != nil {
		return err
	}
	if dropped && vp != nil {
		a.releaseDroppedCheckoutClaim(vp, org)
		log.Infow("process top-up checkout dropped", "processId", processID.Hex(), "sessionId", sessionID)
	}
	return nil
}

// publishPaidProcess publishes a process whose top-up was just credited and consumed, as the
// user who requested the checkout (see applyProcessTopUp). startProcessPublish's claim is what
// keeps it from double-publishing, against a replay as against a concurrent manual publish. It
// runs inside the webhook request and can wait on the organization's tx lock; a delivery that
// times out meanwhile is retried by Stripe and refused by that claim.
//
// It publishes only at the price the checkout was opened for or less: a draft whose price rose
// since (its census grew) is left for a manual publish, never debited more than the customer
// agreed to. Every refusal that needs the user — price rose, wallet short, draft changed, deleted
// or already published, preflight — leaves the credit in the wallet and returns nil. A failure a
// retry can clear (a publish already in flight, a read error) is returned instead, so the
// webhook answers 500 and Stripe redelivers the event. A full tx queue is not one: the publish is
// accepted and retried by the service itself.
func (a *API) publishPaidProcess(t stripe.ProcessTopUp) error {
	processID := t.ProcessID
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("topped-up process not found for publication", "processId", processID.Hex())
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get topped-up process: %w", err)
	}
	if vp.Published {
		return nil
	}
	payment, err := a.db.ProcessPayment(processID)
	if errors.Is(err, db.ErrNotFound) {
		return nil // cancelled while the top-up landed: the credit stays
	}
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	waiting := payment.Status == db.ProcessPaymentPending && payment.Consumed
	if payment.CheckoutSessionID != t.SessionID || (!waiting && payment.Status != db.ProcessPaymentPaid) {
		return nil
	}
	// publishPreflightProblems below re-checks that this user is still an admin of the org
	user, err := a.db.UserByEmail(payment.RequestedBy)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("topped-up process cannot auto-publish: requesting user not found, publish manually",
			"processId", processID.Hex(), "requestedBy", payment.RequestedBy)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get requesting user: %w", err)
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("topped-up process census not found, publish manually", "processId", processID.Hex())
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	// Re-price before publishing. A Stripe retry can land days after the payment, and the
	// census behind the draft can have grown in between (memberbase changes propagate to a
	// group-backed census).
	quote, _, err := a.processQuote(vp)
	if err != nil {
		return fmt.Errorf("failed to re-price topped-up process: %w", err)
	}
	if quote.TotalCents > payment.AmountCents {
		log.Warnw("topped-up process is now priced above its checkout, the credit stays for a manual publish",
			"processId", processID.Hex(), "checkoutCents", payment.AmountCents, "quotedCents", quote.TotalCents)
		return nil
	}
	if err := a.paymentDueForPublish(vp); err != nil {
		if refusedForUser(err) {
			log.Warnw("topped-up process refused publication, the credit stays for a manual publish",
				"processId", processID.Hex(), "error", err)
			return nil
		}
		return fmt.Errorf("failed to re-check payment of topped-up process: %w", err)
	}
	target := publishTarget{vp: vp, questions: questions, census: census, user: user}
	if problems, _ := a.publishPreflightProblems(target); len(problems) > 0 {
		log.Warnw("topped-up process failed publish preflight, the credit stays for a manual publish",
			"processId", processID.Hex(), "problems", strings.Join(problems, "; "))
		return nil
	}
	jobID, err := a.startProcessPublish(target)
	switch {
	case errors.Is(err, errProcessAlreadyPublished):
		return nil
	case refusedForUser(err):
		log.Warnw("topped-up process refused publication, the credit stays for a manual publish",
			"processId", processID.Hex(), "error", err)
		return nil
	case err != nil:
		return fmt.Errorf("failed to start publication of topped-up process: %w", err)
	}
	log.Infow("topped-up process publication enqueued", "processId", processID.Hex(), "jobId", jobID)
	return nil
}

// refusedForUser reports whether a payment-path error is a refusal only the user can clear (the
// webhook answers 200 and leaves the credit), as opposed to one a retry may get past.
func refusedForUser(err error) bool {
	return errors.Is(err, errors.ErrPaymentSessionConflict) || errors.Is(err, errors.ErrQuoteRequired) ||
		errors.Is(err, errors.ErrPaymentRequired) || errors.Is(err, errors.ErrInsufficientWalletBalance) ||
		errors.Is(err, errors.ErrManagedOrgSpendCapReached)
}
