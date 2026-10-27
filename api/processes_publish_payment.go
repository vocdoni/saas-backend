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
// difference. Refused without touching the balance when the wallet does not cover it.
func (a *API) debitProcessWallet(vp *db.VotingProcess, org *db.Organization) error {
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
		errors.Is(err, errors.ErrQuoteRequired) {
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
	if err := a.db.DebitWalletForProcess(db.WalletDebit{
		OrgAddress:        payer,
		ProcessID:         c.vp.ID,
		ProcessOrgAddress: c.vp.OrgAddress,
		AmountCents:       dueCents,
		PriceCents:        c.quote.TotalCents,
	}); err != nil {
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
		ProcessID:   c.vp.ID,
		OrgAddress:  c.vp.OrgAddress,
		Payer:       payer,
		AmountCents: c.quote.TotalCents,
		Currency:    "eur",
		Branding:    c.branding,
		CustomURL:   c.customURL,
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

// publishPaidProcess is the webhook fulfillment hook (stripe.Service.OnProcessPaid): a
// verified payment landed, publish the process as the user who requested the checkout.
// It also fires on Stripe's replays of the fulfillment, so it cannot rely on running once:
// startProcessPublish's claim is what keeps it from double-publishing, against a replay as
// against a concurrent manual publish. It runs inside the webhook request and can wait on
// the organization's tx lock; a delivery that times out meanwhile is retried by Stripe and
// refused by that claim.
//
// A refusal that needs the user leaves the process paid for a manual publish — free, never
// a second charge — and returns nil. A failure a retry can clear (a full tx queue, a publish
// already in flight, a read error) is returned instead, so the webhook answers 500 and
// Stripe redelivers the event: nothing else would ever come back to publish it.
func (a *API) publishPaidProcess(processID bson.ObjectID) error {
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("paid process not found for publication", "processId", processID.Hex())
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get paid process: %w", err)
	}
	if vp.Published {
		return nil
	}
	payment, err := a.db.ProcessPayment(processID)
	if err != nil {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	// publishPreflightProblems below re-checks that this user is still an admin of the org
	user, err := a.db.UserByEmail(payment.RequestedBy)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("paid process cannot auto-publish: requesting user not found, publish manually",
			"processId", processID.Hex(), "requestedBy", payment.RequestedBy)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get requesting user: %w", err)
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("paid process census not found, publish manually", "processId", processID.Hex())
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	// Re-price before publishing. A Stripe retry can land days after the payment, and the
	// census behind the draft can have grown in between (memberbase changes propagate to a
	// group-backed census), so publishing on the strength of the paid status alone would put
	// a bigger election on chain at the smaller price.
	if err := a.paymentDueForPublish(vp); err != nil {
		if refusedForUser(err) {
			log.Warnw("paid process payment refuses publication, staying paid for a manual publish",
				"processId", processID.Hex(), "paidCents", payment.AmountCents, "error", err)
			return nil
		}
		return fmt.Errorf("failed to re-price paid process: %w", err)
	}
	target := publishTarget{vp: vp, questions: questions, census: census, user: user}
	if problems, _ := a.publishPreflightProblems(target); len(problems) > 0 {
		log.Warnw("paid process failed publish preflight, staying paid for a manual publish",
			"processId", processID.Hex(), "problems", strings.Join(problems, "; "))
		return nil
	}
	jobID, err := a.startProcessPublish(target)
	switch {
	case errors.Is(err, errProcessAlreadyPublished):
		return nil
	case errors.Is(err, errors.ErrPaymentSessionConflict):
		log.Warnw("paid process payment refuses publication, staying paid for a manual publish",
			"processId", processID.Hex(), "error", err)
		return nil
	case err != nil:
		return fmt.Errorf("failed to start publication of paid process: %w", err)
	}
	log.Infow("paid process publication enqueued", "processId", processID.Hex(), "jobId", jobID)
	return nil
}

// refusedForUser reports whether a payment-path error is a refusal only the user can clear (the
// webhook answers 200 and leaves the credit), as opposed to one a retry may get past.
func refusedForUser(err error) bool {
	return errors.Is(err, errors.ErrPaymentSessionConflict) || errors.Is(err, errors.ErrQuoteRequired) ||
		errors.Is(err, errors.ErrPaymentRequired) || errors.Is(err, errors.ErrInsufficientWalletBalance)
}
