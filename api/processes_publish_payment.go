package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
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
func walletShortError(s walletShort) error {
	if s.org.ManagedBy != (common.Address{}) {
		return errors.ErrInsufficientWalletBalance.WithData(map[string]pricing.Cents{
			"requiredCents":  s.dueCents,
			"availableCents": s.balanceCents,
		})
	}
	required := &apicommon.ProcessPaymentRequired{
		Quote:          s.quote,
		BalanceCents:   s.balanceCents,
		ShortfallCents: s.dueCents - s.balanceCents,
	}
	if s.paidCents > 0 {
		// a paid draft that outgrew its price has no process checkout to open (409): it buys the
		// difference for the census it has now
		required.PaidCents, required.CensusSize = s.paidCents, s.censusSize
		return errors.ErrPaymentRequired.Withf("the process paid %d eur cents and is now priced at %d; buy the "+
			"difference with POST /processes/{processId}/census/checkout (censusSize %d)",
			s.paidCents, s.quote.TotalCents, s.censusSize).WithData(required)
	}
	return errors.ErrPaymentRequired.WithData(required)
}

// walletShort is what walletShortError reports: dueCents of quote, against the payer's
// balanceCents.
type walletShort struct {
	org          *db.Organization
	quote        pricing.Quote
	dueCents     pricing.Cents
	balanceCents pricing.Cents
	// paidCents and censusSize, set when the process already paid part of quote: the census it
	// is priced at, which census/checkout takes
	paidCents  pricing.Cents
	censusSize int64
}

// processPaid is what a process has paid (ProcessLedgerState, its payer's wallet ledger folded)
// together with its payment row, which holds the open checkout's state and the non-wallet base of a
// published process. Every money decision reads it under the payer's wallet lock; nothing reads a
// paid amount off the row.
type processPaid struct {
	*db.ProcessLedgerState
	// row is the process's payment row, nil when it has none
	row *db.ProcessPayment
}

// hasBase reports whether the process was published and its non-wallet base recorded.
func (p processPaid) hasBase() bool { return p.row != nil && p.row.BaseCents != nil }

// cents is what the process paid: its ledger's envelope plus the non-wallet base.
func (p processPaid) cents() pricing.Cents {
	if p.hasBase() {
		return p.PaidCents + *p.row.BaseCents
	}
	return p.PaidCents
}

// paid reports whether the process holds a payment: a wallet envelope, or a recorded base (a
// published process, even one that paid nothing).
func (p processPaid) paid() bool { return p.PaidCents > 0 || p.hasBase() }

// openCheckout reports whether a top-up checkout opened for the process may still be paid.
func (p processPaid) openCheckout() bool {
	return p.row != nil && p.row.Status == db.ProcessPaymentPending && !p.row.Consumed
}

// processPaidState reads what the process paid from payer's wallet ledger (draining it first) and
// its payment row. A state it cannot read is an error, never taken as unpaid.
func (a *API) processPaidState(processID bson.ObjectID, payer common.Address) (processPaid, error) {
	state, err := a.db.ProcessLedgerState(payer, processID)
	if err != nil {
		return processPaid{}, fmt.Errorf("failed to read the process ledger: %w", err)
	}
	return a.withPaymentRow(processID, state)
}

// withPaymentRow pairs a process's ledger state with its payment row as stored now. A caller
// holding the payer's wallet lock reuses the state it read under it, since every ledger movement of
// a process is made under that lock, but reads the row again: a checkout is opened, consumed and
// dropped outside it.
func (a *API) withPaymentRow(processID bson.ObjectID, state *db.ProcessLedgerState) (processPaid, error) {
	paid := processPaid{ProcessLedgerState: state}
	row, err := a.db.ProcessPayment(processID)
	switch {
	case err == nil:
		paid.row = row
	case !errors.Is(err, db.ErrNotFound):
		return processPaid{}, fmt.Errorf("failed to get process payment: %w", err)
	default:
	}
	return paid, nil
}

// paymentDueForPublish is the publish gate: it refuses, before the publish is claimed, what the
// debit inside startProcessPublish would refuse anyway, with the answer the client needs. Nil
// means clear to publish: the process is free, already paid, or its payer's wallet covers what is
// due. Otherwise: ErrQuoteRequired (422) for a quote-only census, 409 while a top-up checkout opened
// for the process is still open, spendCapError (402, 40183) when a managed organization's debit
// would pass its spend cap, and walletShortError (402) when the wallet falls short. Only a
// courtesy: the balance can move before the debit, whose own refusal is the authoritative answer.
// It also answers 409 while another process holds a once-per-organization add-on the draft
// selects (quotedDraft.heldError), or while one it selects is left unpriced with the organization
// not stamped for it (unstampedOrgAddOnError). It only reads: whatever a credit-back still owes is applied by
// the debit, under the payer's wallet lock.
func (a *API) paymentDueForPublish(vp *db.VotingProcess) error {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	paid, err := a.processPaidState(vp.ID, walletPayer(org))
	if err != nil {
		return err
	}
	if paid.Refunded {
		return errRefundedDraft
	}
	quote, quoted, err := a.quoteDraft(draftQuote{vp: vp, paid: &paid})
	if err != nil {
		return err
	}
	// quote-only: there is no price, and its zero total must never read as free
	if quote.QuoteRequired {
		return errors.ErrQuoteRequired
	}
	if err := quoted.heldError(); err != nil {
		return err
	}
	if err := a.unstampedOrgAddOnError(vp, &quoted); err != nil {
		return err
	}
	if paid.openCheckout() {
		// publishing over it would leave a payable session behind (even for a draft that shrank
		// to free): the customer pays it, or replaces or cancels it first
		if paid.row.AmountCents != quote.TotalCents {
			return errors.ErrPaymentSessionConflict.Withf("a wallet top-up checkout is open for the process, opened "+
				"for %d eur cents, and its price changed since (now %d): call POST /processes/{processId}/checkout "+
				"again to replace it, or cancel it with DELETE /processes/{processId}/checkout and publish again",
				paid.row.AmountCents, quote.TotalCents)
		}
		return errors.ErrPaymentSessionConflict.Withf("a wallet top-up checkout is open for the process; " +
			"pay it, or cancel it with DELETE /processes/{processId}/checkout, and publish again")
	}
	paidCents := paid.cents()
	dueCents := quote.TotalCents - paidCents
	if dueCents <= 0 {
		return nil // free, or covered by what was paid
	}
	wallet, err := a.db.Wallet(walletPayer(org))
	if err != nil {
		return fmt.Errorf("failed to get wallet: %w", err)
	}
	// the debit checks the managed organization's spend cap before the balance, and so does this
	if org.ManagedBy != (common.Address{}) {
		if err := spendCapError(wallet.ManagedOrgSpend(org.Address), dueCents); err != nil {
			return err
		}
	}
	if wallet.BalanceCents >= dueCents {
		return nil
	}
	return walletShortError(walletShort{
		org: org, quote: quote, dueCents: dueCents, balanceCents: wallet.BalanceCents,
		paidCents: paidCents, censusSize: int64(quoted.CensusSize),
	})
}

// spendCapError is the 402 (40183) of a debit of dueCents that would take a managed organization
// past the spend cap its integrator set, with capCents, spentCents and requiredCents; nil when the
// debit stays within it, or there is no cap.
func spendCapError(spend db.ManagedOrgSpend, dueCents pricing.Cents) error {
	if spend.CapCents == nil || spend.SpentCents+dueCents <= *spend.CapCents {
		return nil
	}
	return errors.ErrManagedOrgSpendCapReached.WithData(map[string]pricing.Cents{
		"capCents":      *spend.CapCents,
		"spentCents":    spend.SpentCents,
		"requiredCents": dueCents,
	})
}

// debitProcessWallet charges a draft's publish to its payer's wallet: atomic, and priced against
// the draft as it is right now. It is the only debit of a publish, and runs inside
// startProcessPublish after the publish claim, so two concurrent publishes cannot both debit and
// a delete cannot refund between the debit and the publish. A publish retry re-prices, so a retry
// of an unchanged draft debits nothing while one whose census grew between the attempts pays the
// difference. A draft whose earlier publish was credited back pays again, in full. Refused
// without touching the balance when the wallet does not cover it. census is the draft's census as
// the publish stores and publishes it, read under the organization's census growth lock: the price
// is that census's, never one a concurrent member add changed. Nil reads it here.
func (a *API) debitProcessWallet(vp *db.VotingProcess, org *db.Organization, census *db.Census) error {
	return a.debitProcessWalletUpTo(publishDebit{vp: vp, org: org, census: census})
}

// publishDebit is what debitProcessWalletUpTo charges: debitProcessWallet's arguments, plus the
// most the publish may cost.
type publishDebit struct {
	vp     *db.VotingProcess
	org    *db.Organization
	census *db.Census
	// ceilingCents, when positive, refuses (40177) a price above it before anything is debited:
	// the top-up webhook's publish never charges more than the checkout was opened for
	ceilingCents pricing.Cents
}

// debitProcessWalletUpTo is debitProcessWallet with a price ceiling.
func (a *API) debitProcessWalletUpTo(d publishDebit) error {
	vp, org, census := d.vp, d.org, d.census
	// quote, claims and debit run under the payer's wallet lock, as a delete's refund decides what
	// to withhold and releases the add-on stamp under it: priced outside, a stamp a concurrent
	// refund then released would publish the add-on for free. The organization is read again
	// under the lock for the same reason.
	// bounded like every lock a request waits on: holders only touch Mongo, so a timeout means a
	// stuck database, and the publish is refused before anything is debited
	payer := walletPayer(org)
	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
	if err != nil {
		return err
	}
	defer walletLock.Unlock()
	// a release an earlier attempt's credit-back still owes lands before this attempt is priced:
	// priced over the stale stamp, the draft would publish the add-on without paying for it. Read
	// once for the whole lock hold: quoting and the charge below reuse it
	paid, err := a.processPaidState(vp.ID, payer)
	if err != nil {
		return err
	}
	if err := a.healOwedRelease(vp, paid); err != nil {
		return err
	}
	org, err = a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	quote, input, err := a.quoteDraft(draftQuote{vp: vp, census: census, paid: &paid})
	if err != nil {
		return err
	}
	if err := input.heldError(); err != nil {
		return err
	}
	// win the add-on claims before the debit: two publishes racing here would otherwise both
	// price a once-per-organization add-on in and both be charged for it
	quote, claims, err := a.claimOrgAddOnsForPayment(vp, &input)
	if err == nil {
		err = a.unstampedOrgAddOnError(vp, &input)
	}
	if err == nil && d.ceilingCents > 0 && quote.TotalCents > d.ceilingCents {
		err = priceAboveCheckoutError(quote.TotalCents, d.ceilingCents)
	}
	if err != nil {
		a.releaseRefusedOrgAddOnClaims(vp, claims)
		return err
	}
	// a quote-only total is not free: chargeProcessWallet refuses it
	if quote.TotalCents == 0 && !quote.QuoteRequired {
		return nil
	}
	_, err = a.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: quote, addOns: pricedOrgAddOns(&input.QuoteInput), walletLocked: true, paid: &paid,
	})
	// refused before the debit, nothing backs the claims; any later failure keeps them, since
	// the debit it follows may already be applied
	if errors.Is(err, errors.ErrInsufficientWalletBalance) || errors.Is(err, errors.ErrPaymentRequired) ||
		errors.Is(err, errors.ErrQuoteRequired) || errors.Is(err, errors.ErrManagedOrgSpendCapReached) ||
		errors.Is(err, errors.ErrDuplicateConflict) {
		a.releaseRefusedOrgAddOnClaims(vp, claims)
	}
	return err
}

// unstampedOrgAddOnError is the 409 (40177) for a once-per-organization add-on vp selects that
// quoted leaves out of the price while the organization is not stamped for it, which would publish
// it for free: a draft paid without it (processQuote never sells one to a paid process) whose edit
// relied on a stamp released since, or a claimant's payment whose stamp could not be written. Only
// the organization's stamp, as it is now, makes an unpriced add-on free; the publish debit asks
// under the payer's wallet lock, where every release of a stamp is made, and the gate asks first.
func (a *API) unstampedOrgAddOnError(vp *db.VotingProcess, quoted *quotedDraft) error {
	unpaid := func(org *db.Organization, addOn db.OrgAddOn) bool {
		return vp.AddOns.Has(addOn) && !*quoteAddOn(&quoted.QuoteInput, addOn) && org.AddOnPaidAt(addOn).IsZero()
	}
	if !slices.ContainsFunc(db.OrgAddOns, func(addOn db.OrgAddOn) bool { return unpaid(quoted.org, addOn) }) {
		return nil
	}
	// read again: quoting may have stamped an add-on its claimant paid for since quoted.org was read
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	for _, addOn := range db.OrgAddOns {
		if unpaid(org, addOn) {
			return errors.ErrPaymentSessionConflict.Withf("the %s add-on is once per organization, the organization "+
				"has not paid for it and the draft's payment does not include it (it cannot be added after payment): "+
				"deselect it, or publish once another process pays for it", addOn)
		}
	}
	return nil
}

// pricedOrgAddOns are the once-per-organization add-ons input prices in.
func pricedOrgAddOns(input *pricing.QuoteInput) []db.OrgAddOn {
	var addOns []db.OrgAddOn
	for _, addOn := range db.OrgAddOns {
		if *quoteAddOn(input, addOn) {
			addOns = append(addOns, addOn)
		}
	}
	return addOns
}

// processCharge is one charge of a process against its payer's wallet: quote is the full price
// the process must end up having paid, and addOns the once-per-organization add-ons quote prices
// in (none for census headroom). fromCents, when set, makes the charge apply only while the
// process has paid exactly that (census headroom bought through a checkout priced from it).
type processCharge struct {
	vp        *db.VotingProcess
	org       *db.Organization
	quote     pricing.Quote
	addOns    []db.OrgAddOn
	fromCents *pricing.Cents
	// walletLocked: the caller already holds the payer's wallet lock
	walletLocked bool
	// paid, with walletLocked, is what the caller read the process paid in that lock hold: its
	// ledger state is reused rather than folded again (see withPaymentRow)
	paid *processPaid
}

// chargeProcessWallet debits the payer's wallet for whatever of c.quote the process has not paid
// yet, so both the publish path and a census that outgrew its price charge the same way: only the
// delta, at most once per (process, attempt, price). Returns walletShortError when the balance
// does not cover it, or ErrQuoteRequired when the price needs a custom quote, without touching the
// wallet. It returns what it debited: 0 when the price was already covered.
//
// What the process paid is read off its payer's wallet ledger inside the payer's wallet lock: two
// headroom purchases racing at different prices would otherwise both charge the delta from the
// same base, and the second would pay for voters the first already bought. The debit is the
// record: there is nothing else to write for the money to be accounted for, and what follows it
// (the add-on stamps, the payment row's display cache) is idempotent and redone by a retry.
func (a *API) chargeProcessWallet(c processCharge) (pricing.Cents, error) {
	// above the self-service limit there is no price, only a custom quote, never a wallet debit.
	// Checked first: a quote-only total of 0 would read as "already covered" by any payment.
	if c.quote.QuoteRequired {
		return 0, errors.ErrQuoteRequired
	}
	payer := walletPayer(c.org)
	// ponytail: in-memory lock, like orgTxLocks — one API instance; a DB lease if that changes
	if !c.walletLocked {
		walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
		if err != nil {
			return 0, err
		}
		defer walletLock.Unlock()
	}
	// checked under the wallet lock the teardown's credits take: a debit landing between its draft
	// sweep and the bulk delete would go with the process, never credited back
	if err := a.refuseTearingDown(c.vp.OrgAddress); err != nil {
		return 0, err
	}
	var paid processPaid
	var err error
	if c.walletLocked && c.paid != nil {
		paid, err = a.withPaymentRow(c.vp.ID, c.paid.ProcessLedgerState)
	} else {
		paid, err = a.processPaidState(c.vp.ID, payer)
	}
	if err != nil {
		return 0, err
	}
	// a census headroom purchase can race a failed publish's reversal: what it owes lands first
	if err := a.healOwedRelease(c.vp, paid); err != nil {
		return 0, err
	}
	if paid.Refunded {
		return 0, errRefundedDraft
	}
	if paid.openCheckout() {
		return 0, errors.ErrPaymentSessionConflict.Withf(
			"process %s has an open top-up checkout the wallet cannot pay for", c.vp.ID.Hex())
	}
	alreadyCents := paid.cents()
	if c.fromCents != nil && alreadyCents != *c.fromCents {
		return 0, errors.ErrPaymentSessionConflict.Withf("process %s paid %d eur cents, not the %d this charge was priced from",
			c.vp.ID.Hex(), alreadyCents, *c.fromCents)
	}
	if alreadyCents >= c.quote.TotalCents {
		// this price is already covered: nothing to debit, but what a charge cut short after its
		// debit left undone is redone
		return 0, a.completeProcessCharge(c.vp, paid.AddOns, alreadyCents)
	}
	dueCents := c.quote.TotalCents - alreadyCents
	// only the add-ons this charge prices in for the first time are its own: a re-priced paid
	// draft already carries the ones it paid
	var addOns []db.OrgAddOn
	for _, addOn := range c.addOns {
		if !paid.Charged(addOn) {
			addOns = append(addOns, addOn)
		}
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
		Attempt:           paid.Attempt,
		AddOns:            addOns,
		ManagedOrg:        managedOrg,
	}); err != nil {
		if errors.Is(err, db.ErrManagedOrgSpendCapReached) {
			if wallet, werr := a.db.Wallet(payer); werr == nil {
				if capErr := spendCapError(wallet.ManagedOrgSpend(c.vp.OrgAddress), dueCents); capErr != nil {
					return 0, capErr
				}
			}
			return 0, errors.ErrManagedOrgSpendCapReached
		}
		if errors.Is(err, db.ErrInsufficientWalletBalance) {
			wallet, werr := a.db.Wallet(payer)
			if werr != nil {
				return 0, fmt.Errorf("wallet short, and its balance could not be read: %w", werr)
			}
			return 0, walletShortError(walletShort{
				org: c.org, quote: c.quote, dueCents: dueCents, balanceCents: wallet.BalanceCents,
			})
		}
		return 0, fmt.Errorf("failed to debit wallet: %w", err)
	}
	// the add-ons the process paid before are stamped again too (idempotent)
	return dueCents, a.completeProcessCharge(c.vp, slices.Concat(paid.AddOns, addOns), alreadyCents+dueCents)
}

// completeProcessCharge is what follows a charge's debit, idempotent so a retry redoes it: stamp
// each once-per-organization add-on of stamps (those the process paid) as paid, so the org's next
// process is not charged it again (conditional in the DB: only the first payment sets it), and
// refresh the payment row's display cache at paidCents.
func (a *API) completeProcessCharge(vp *db.VotingProcess, stamps []db.OrgAddOn, paidCents pricing.Cents) error {
	if err := atMoneyStep(moneyStepStamp, vp.ID); err != nil {
		return err
	}
	for _, addOn := range stamps {
		if _, err := a.db.SetOrganizationAddOnPaid(vp.OrgAddress, addOn, time.Now()); err != nil {
			log.Warnw("could not stamp organization add-on paid", "processId", vp.ID.Hex(),
				"orgAddress", vp.OrgAddress.String(), "addOn", addOn, "error", err)
		}
	}
	if err := atMoneyStep(moneyStepCache, vp.ID); err != nil {
		return err
	}
	if paidCents > 0 {
		a.refreshPaymentCache(vp, db.ProcessPaymentPaid, paidCents)
	}
	return nil
}

// refreshPaymentCache writes the payment row's display cache (status, amountCents) after a money
// path moved what the process paid. Only logged on failure: nothing reads it for money, and the
// next money path refreshes it again.
func (a *API) refreshPaymentCache(vp *db.VotingProcess, status db.ProcessPaymentStatus, amountCents pricing.Cents) {
	if err := a.db.SetProcessPaymentCache(db.SetProcessPaymentCacheParams{
		ProcessID: vp.ID, OrgAddress: vp.OrgAddress, Status: status, AmountCents: amountCents,
	}); err != nil {
		log.Warnw("could not refresh the process payment status", "processId", vp.ID.Hex(), "status", status, "error", err)
	}
}

// moneyStep names the writes a money path makes after its wallet movement, for moneyStepHook.
type moneyStep int

const (
	// moneyStepStamp: a charge stamps the organization with the add-ons it paid.
	moneyStepStamp moneyStep = iota + 1
	// moneyStepCache: a money path refreshes the payment row's display cache.
	moneyStepCache
	// moneyStepRelease: a credit-back releases the add-ons it gives back.
	moneyStepRelease
	// moneyStepPaymentDrop: a delete drops the draft's payment row.
	moneyStepPaymentDrop
	// moneyStepDraftDelete: a delete drops the draft.
	moneyStepDraftDelete
)

// moneyStepHook, when set (tests only), runs before each step of a money path that follows its
// wallet movement; an error it returns stops the path there, as a crash would.
var moneyStepHook func(step moneyStep, processID bson.ObjectID) error

// atMoneyStep runs moneyStepHook, if set.
func atMoneyStep(step moneyStep, processID bson.ObjectID) error {
	if moneyStepHook == nil {
		return nil
	}
	return moneyStepHook(step, processID)
}

// reversePublishDebit gives a draft's wallet payment back to its payer when its publish failed
// for good before any election was mined: the publish worker's abandon, the publish request
// failing after the debit, and the stale-marker sweep after a restart. The caller holds the
// publish claim. A draft with mined elections keeps the debit — they exist and were paid for, and
// publishing again resumes them for free. So does a draft with a submitted publish tx the chain
// has not settled yet (resolvePublishTxs): its election may exist or still mine, and it stays
// paid, the state a delete refunds and a publish reuses, until a later touch resolves it.
//
// Under the payer's wallet lock it reads what the process paid off the ledger and credits it back
// under the attempt's key (db.PublishFailCreditKey), the credit-back row carrying the add-ons it
// gives back; that one write makes the draft unpaid, moves its attempt on (the next publish debits
// under keys no earlier attempt applied) and records the release it owes. The release, pinned to
// claims taken before the credit-back, follows (healOwedRelease), and so does the payment row's
// display cache; whatever a crash leaves undone of them is redone by whatever touches the process
// next: another reversal, the next publish's debit, a checkout, or the draft's delete. A retry
// finds nothing paid and credits nothing. Once-per-organization add-ons another process already
// relies on are withheld and stay the organization's, as a delete's credit-back withholds them; a
// payment those add-ons account for entirely gives nothing back and is left paid.
func (a *API) reversePublishDebit(processID bson.ObjectID) error {
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		return fmt.Errorf("failed to get process: %w", err)
	}
	if vp.Published {
		return nil
	}
	// a submitted tx that mined is adopted here, and then counts as mined below; one that may
	// still mine, or that the chain cannot tell about, keeps the debit until a later touch
	// resolves it
	unresolved, err := a.resolvePublishTxs(vp, questions)
	if err != nil {
		return fmt.Errorf("failed to resolve submitted publish txs: %w", err)
	}
	if unresolved || vp.PublishSubmitted {
		log.Warnw("failed publish kept paid: a submitted election may exist", "processId", processID.Hex(),
			"txHashes", publishTxHashes(questions))
		return nil
	}
	// HasMinedElections also covers an election that mined but whose id never reached its question
	if anyMined(questions) || vp.HasMinedElections {
		return nil
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	payer := walletPayer(org)
	// unbounded, unlike the request paths: a credit-back is never given up, and holders only
	// touch Mongo
	walletLock := a.walletLocks.lock(payer)
	defer walletLock.Unlock()
	paid, err := a.processPaidState(processID, payer)
	if err != nil {
		return err
	}
	if paidCents := paid.PaidCents; paidCents > 0 && !paid.Refunded {
		withheld, err := a.reliedOnOrgAddOns(vp, paid.ProcessLedgerState, nil)
		if err != nil {
			return err
		}
		owed := paidCents - withheld.Cents
		if owed <= 0 {
			// nothing to give back: the payment stays, and so do the add-ons it bought, which a
			// publish again finds paid
			log.Infow("failed publish paid only for add-ons in use, kept paid", "processId", processID.Hex(),
				"amountCents", paidCents, "withheldAddOns", withheld.AddOns)
			return nil
		}
		if err := a.creditProcessBack(processCreditBack{
			vp: vp, payer: payer, key: db.PublishFailCreditKey(processID, paid.Attempt),
			amountCents: owed, withheld: withheld, addOns: paid.AddOns,
		}); err != nil {
			return err
		}
		log.Infow("failed publish credited back to the payer's wallet", "processId", processID.Hex(),
			"payer", payer.String(), "amountCents", owed, "attempt", paid.Attempt)
		if paid, err = a.processPaidState(processID, payer); err != nil {
			return err
		}
		if err := a.healOwedRelease(vp, paid); err != nil {
			return err
		}
		if err := atMoneyStep(moneyStepCache, processID); err != nil {
			return err
		}
		a.refreshPaymentCache(vp, db.ProcessPaymentFailed, paidCents)
		return nil
	}
	return a.healOwedRelease(vp, paid)
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
// or reverted — stops it, and so does a ledger that credited the draft back after this top-up,
// whatever the row says. So a Stripe retry never debits a draft whose payment was given back.
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
	// headroom prices in no add-on of its own: it never stamps a new one
	from := t.FromCents
	debited, err := a.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: t.HeadroomCents}, fromCents: &from,
	})
	// a busy wallet is not a refusal: Stripe redelivers, and the charge is keyed
	if errors.Is(err, errors.ErrServerBusy) {
		return err
	}
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
// Each refusal is recorded on the payment (refuseTopUpPublish), so GET
// /processes/{processId}/checkout tells "credited, publish manually" from "not processed yet":
// those below, a requester gone or no longer an admin, an add-on another process holds, and a
// census that cannot be priced. A deleted or already published process records nothing.
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
	// the row's status is a display cache a lost refresh can leave behind, so whether this
	// session's publish was already given back is read off the ledger: a credit-back after the
	// top-up's credit means the publish it paid for failed and was credited back, and a
	// redelivery must not debit and publish the draft again
	if back, err := a.topUpPublishCreditedBack(vp, t.SessionID); err != nil || back {
		return err
	}
	// publishPreflightProblems below re-checks that this user is still an admin of the org
	user, err := a.db.UserByEmail(payment.RequestedBy)
	if errors.Is(err, db.ErrNotFound) {
		return a.refuseTopUpPublish(t, errors.ErrUserNotFound.Withf(
			"the user who opened the checkout no longer exists; an admin publishes the process"))
	}
	if err != nil {
		return fmt.Errorf("failed to get requesting user: %w", err)
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if errors.Is(err, db.ErrNotFound) {
		return a.refuseTopUpPublish(t, errors.ErrCensusNotFound)
	}
	if err != nil {
		return fmt.Errorf("failed to get census: %w", err)
	}
	// Re-price before publishing. A Stripe retry can land days after the payment, and the
	// census behind the draft can have grown in between (memberbase changes propagate to a
	// group-backed census).
	quote, quoted, err := a.processQuote(vp)
	if refusedForUser(err) {
		return a.refuseTopUpPublish(t, err) // a census that cannot be priced
	}
	if err != nil {
		return fmt.Errorf("failed to re-price topped-up process: %w", err)
	}
	if err := quoted.heldError(); err != nil {
		return a.refuseTopUpPublish(t, err)
	}
	if quote.TotalCents > payment.AmountCents {
		return a.refuseTopUpPublish(t, priceAboveCheckoutError(quote.TotalCents, payment.AmountCents))
	}
	if err := a.paymentDueForPublish(vp); err != nil {
		if refusedForUser(err) {
			return a.refuseTopUpPublish(t, err)
		}
		return fmt.Errorf("failed to re-check payment of topped-up process: %w", err)
	}
	// the check above read the census outside the growth lock: the debit prices it again under
	// the lock, and refuses past the same ceiling
	target := publishTarget{vp: vp, questions: questions, census: census, user: user, ceilingCents: payment.AmountCents}
	if err := preflightError(a.publishPreflightProblems(target)); err != nil {
		return a.refuseTopUpPublish(t, err)
	}
	// no request context to bound the org tx lock wait: it is bounded by orgTxLockTimeout, and
	// a busy lock (503) is returned like any other retryable failure
	jobID, err := a.startProcessPublish(context.Background(), target)
	switch {
	case errors.Is(err, errProcessAlreadyPublished):
		return nil
	case refusedForUser(err):
		return a.refuseTopUpPublish(t, err)
	case err != nil:
		return fmt.Errorf("failed to start publication of topped-up process: %w", err)
	}
	log.Infow("topped-up process publication enqueued", "processId", processID.Hex(), "jobId", jobID)
	return nil
}

// priceAboveCheckoutError is the top-up webhook's refusal of a process now priced above what its
// checkout was opened for.
func priceAboveCheckoutError(totalCents, checkoutCents pricing.Cents) error {
	return errors.ErrPaymentSessionConflict.Withf(
		"the process is now priced at %d eur cents, above the %d its checkout was opened for; publish it manually",
		totalCents, checkoutCents)
}

// topUpPublishCreditedBack reports whether the ledger credited vp back after the top-up of
// sessionID landed: the publish that top-up paid for was given back (or the draft refunded).
func (a *API) topUpPublishCreditedBack(vp *db.VotingProcess, sessionID string) (bool, error) {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return false, fmt.Errorf("failed to get organization: %w", err)
	}
	payer := walletPayer(org)
	state, err := a.db.ProcessLedgerState(payer, vp.ID)
	if err != nil {
		return false, fmt.Errorf("failed to read the process ledger: %w", err)
	}
	if state.CreditedBackAt.IsZero() {
		return false, nil
	}
	topUp, err := a.db.WalletEntry(payer, sessionID)
	if err != nil {
		return false, fmt.Errorf("failed to read the process top-up: %w", err)
	}
	if !state.CreditedBackAt.After(topUp.CreatedAt) {
		return false, nil
	}
	log.Infow("top-up's publish already credited back, the credit stays in the wallet",
		"processId", vp.ID.Hex(), "sessionId", sessionID)
	return true, nil
}

// refuseTopUpPublish records on the payment why the top-up webhook did not publish the process of
// t, so GET /processes/{processId}/checkout can tell "credited, publish manually" from "not
// processed yet". The credit stays in the wallet. Only a failure to record it is returned: the
// webhook answers 500, and Stripe's redelivery meets the same refusal and records it again.
func (a *API) refuseTopUpPublish(t stripe.ProcessTopUp, refusal error) error {
	log.Warnw("topped-up process not published, the credit stays for a manual publish",
		"processId", t.ProcessID.Hex(), "sessionId", t.SessionID, "reason", refusal.Error())
	found, err := a.db.SetProcessPaymentTopUpRefusal(t.ProcessID, t.SessionID, topUpRefusal(refusal))
	if err != nil {
		return fmt.Errorf("failed to record why the topped-up process was not published: %w", err)
	}
	if !found {
		log.Infow("refusal not recorded: the payment no longer backs this session",
			"processId", t.ProcessID.Hex(), "sessionId", t.SessionID)
	}
	return nil
}

// topUpRefusal is the client-facing record of a refusal: its code and message, or, for a
// server-side failure whose detail must not reach the client, a generic one (the detail is logged).
func topUpRefusal(refusal error) db.TopUpRefusal {
	var apiErr errors.Error
	if errors.As(refusal, &apiErr) && apiErr.HTTPstatus < http.StatusInternalServerError {
		return db.TopUpRefusal{Code: apiErr.Code, Message: apiErr.Error()}
	}
	return db.TopUpRefusal{
		Code:    errors.ErrGenericInternalServerError.Code,
		Message: "the process could not be published; the payment stays in the wallet, publish the process manually",
	}
}

// refusedForUser reports whether a payment-path error is a refusal only the user can clear (the
// webhook records it and answers 200, leaving the credit), as opposed to one a retry may get past.
// It is an explicit list: a status-based rule would take ErrPublishInProgress (409, the webhook's
// own guard against a concurrent publish) for a refusal. ErrOrgTxBusy, server errors and plain
// errors are retried.
func refusedForUser(err error) bool {
	// the payment was credited back: Stripe redelivering would publish nothing
	if afterDebit := (errPublishFailedAfterDebit{}); errors.As(err, &afterDebit) {
		return true
	}
	// the draft, its census or the organization has to change before a publish can pass these
	if errors.Is(err, errors.ErrMalformedBody) || errors.Is(err, errors.ErrProcessQuestionsMismatch) ||
		errors.Is(err, errors.ErrStaleUpdate) || errors.Is(err, errors.ErrDuplicateConflict) {
		return true
	}
	return errors.Is(err, errors.ErrPaymentSessionConflict) || errors.Is(err, errors.ErrQuoteRequired) ||
		errors.Is(err, errors.ErrPaymentRequired) || errors.Is(err, errors.ErrInsufficientWalletBalance) ||
		errors.Is(err, errors.ErrManagedOrgSpendCapReached)
}
