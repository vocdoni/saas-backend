package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// paymentDueForPublish decides whether publication must be refused for lack of payment.
// A non-nil quote is what is still owed (answer 402 with it). Nil means clear to
// publish: the process is free, already paid, or managed (its integrator wallet is
// debited inside the publish path instead). A free process with a checkout still open or
// processing is refused with a 409 errors.Error instead.
func (a *API) paymentDueForPublish(vp *db.VotingProcess) (*pricing.Quote, error) {
	if err := a.refuseRefundedDraft(vp.ID); err != nil {
		return nil, err
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}
	if org.ManagedBy != (common.Address{}) {
		return nil, nil
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		return nil, err
	}
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("failed to get process payment: %w", err)
	}
	if quote.TotalCents == 0 {
		// a draft that shrank to free while a checkout was open must not publish over it:
		// the session stays payable, and paying it would charge for a free process
		if err == nil && (payment.Status == db.ProcessPaymentPending || payment.Status == db.ProcessPaymentProcessing) {
			return nil, errors.ErrPaymentSessionConflict.Withf("the process is free but its payment is %s; "+
				"cancel it with DELETE /processes/{processId}/checkout (or wait for it to settle) and publish again",
				payment.Status)
		}
		return nil, nil
	}
	if errors.Is(err, db.ErrNotFound) {
		return &quote, nil
	}
	if payment.Status != db.ProcessPaymentPaid {
		return &quote, nil
	}
	if payment.QuoteHash == pricing.QuoteHash(input, quote.TotalCents) {
		return nil, nil
	}
	// The draft no longer matches what was paid for. This is not just a race:
	// refusePaymentLocked covers the process endpoints, but the census behind a paid
	// draft can still be grown through POST /census/{id}, which has no payment state to
	// consult — so the extra voters would otherwise ride on the smaller price.
	if quote.TotalCents > payment.AmountCents {
		log.Warnw("paid process is now priced above its payment, refusing publication",
			"processId", vp.ID.Hex(), "paidCents", payment.AmountCents, "quotedCents", quote.TotalCents)
		return &quote, nil
	}
	// It is not worth more than was paid. The money was taken and paid is terminal, so
	// refusing would strand it with nothing the user could do; publish and log it.
	log.Warnw("paid quote does not match the current draft, publishing anyway",
		"processId", vp.ID.Hex(), "paidCents", payment.AmountCents, "quotedCents", quote.TotalCents)
	return nil, nil
}

// debitManagedProcessWallet charges a managed organization's process to its integrator's
// prepaid wallet: atomic, and priced against the draft as it is right now. A publish retry
// re-prices, so a retry of an unchanged draft debits nothing while one whose census grew
// between the attempts is topped up by the difference — the price is never fixed by the
// first attempt. Refused without touching the balance when the wallet does not cover it.
// The paid state is recorded afterwards; the wallet document itself is the authoritative
// record, so a failure there only logs.
func (a *API) debitManagedProcessWallet(vp *db.VotingProcess, org *db.Organization) error {
	quote, input, err := a.processQuote(vp)
	if err != nil {
		return err
	}
	// win the branding claim before the debit: two managed publishes racing here would
	// otherwise both price branding in and both be charged for it
	quote, claimedAt, err := a.claimBrandingForPayment(vp, org, &input)
	if err != nil {
		a.releaseRefusedBrandingClaim(vp, claimedAt)
		return err
	}
	if quote.TotalCents == 0 {
		return nil
	}
	_, err = a.chargeManagedProcessWallet(managedProcessCharge{
		vp: vp, org: org, quote: quote, branding: input.Branding,
	})
	// refused before the debit, nothing backs the claim; any later failure keeps it, since
	// the debit it follows may already be applied
	if errors.Is(err, errors.ErrInsufficientWalletBalance) || errors.Is(err, errors.ErrQuoteRequired) {
		a.releaseRefusedBrandingClaim(vp, claimedAt)
	}
	return err
}

// managedProcessCharge is one charge of a managed organization's process against its
// integrator's prepaid wallet: quote is the full price the process must end up having paid,
// and branding whether quote prices the once-per-organization add-on in.
type managedProcessCharge struct {
	vp       *db.VotingProcess
	org      *db.Organization
	quote    pricing.Quote
	branding bool
}

// chargeManagedProcessWallet debits the integrator wallet for whatever of c.quote is not
// covered yet and records the new price, so both the publish path and a census that outgrew
// its price charge the same way: only the delta, at most once per (process, price). Returns a
// typed ErrInsufficientWalletBalance carrying the shortfall when the balance does not cover
// it, or ErrQuoteRequired when the price needs a custom quote, without touching the wallet.
// It returns what it debited: 0 when the price was already covered.
//
// Charges to one wallet run one at a time, and what the process paid already is read inside
// that lock: two headroom purchases racing at different prices would otherwise both charge
// the delta from the same base, and the second would pay for voters the first already bought.
//
// The debit and the paid record cannot be one write, so the record is not optional: a debit
// whose record is lost would leave the process looking free, and its next growth or refund
// would be computed from nothing. A card-owned record the wallet may not overwrite is refused
// before any money moves; a record write that still fails is an error the caller must stop on,
// and a retry heals it, since the debit key is already applied and charges nothing.
func (a *API) chargeManagedProcessWallet(c managedProcessCharge) (int64, error) {
	// ponytail: in-memory lock, like orgTxLocks — one API instance; a DB CAS if that changes
	walletLock := a.walletLocks.lock(c.org.ManagedBy)
	defer walletLock.Unlock()
	// A payment state we cannot read is never assumed absent: that would debit the whole
	// price a second time.
	paid, err := a.db.ProcessPayment(c.vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return 0, fmt.Errorf("failed to get process payment: %w", err)
	}
	if err == nil && !walletMayRecord(paid) {
		return 0, errors.ErrPaymentSessionConflict.Withf(
			"process %s has a %s payment the integrator wallet cannot pay for", c.vp.ID.Hex(), paid.Status)
	}
	var alreadyCents int64
	if err == nil && paid.Status == db.ProcessPaymentPaid {
		alreadyCents = paid.AmountCents
	}
	if alreadyCents >= c.quote.TotalCents {
		return 0, nil // this price is already covered
	}
	// above the self-service limit a price is a custom quote, never a wallet debit: the same
	// refusal card checkout answers, so a managed organization cannot self-serve past it either
	if c.quote.QuoteRequired {
		return 0, errors.ErrQuoteRequired
	}
	dueCents := c.quote.TotalCents - alreadyCents
	if err := a.db.DebitWalletForProcess(db.WalletDebit{
		OrgAddress:  c.org.ManagedBy,
		ProcessID:   c.vp.ID,
		AmountCents: dueCents,
		PriceCents:  c.quote.TotalCents,
	}); err != nil {
		if errors.Is(err, db.ErrInsufficientWalletBalance) {
			wallet, werr := a.db.Wallet(c.org.ManagedBy)
			if werr != nil {
				return 0, errors.ErrInsufficientWalletBalance
			}
			return 0, errors.ErrInsufficientWalletBalance.WithData(map[string]int64{
				"requiredCents":  dueCents,
				"availableCents": wallet.BalanceCents,
			})
		}
		return 0, fmt.Errorf("failed to debit integrator wallet: %w", err)
	}
	walletPayment := &db.ProcessPayment{
		ProcessID:   c.vp.ID,
		OrgAddress:  c.vp.OrgAddress,
		AmountCents: c.quote.TotalCents,
		Currency:    "eur",
		Branding:    c.branding,
	}
	if paid != nil {
		// keep the record's history across a top-up
		walletPayment.CreatedAt = paid.CreatedAt
		walletPayment.PaidAt = paid.PaidAt
		walletPayment.RequestedBy = paid.RequestedBy
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
	// stamp the once-per-organization branding add-on as paid when the debited quote
	// carried it, so the org's next process is not charged branding again (conditional
	// in the DB: only the first payment sets it).
	if c.branding {
		if _, err := a.db.SetOrganizationBrandingPaid(c.vp.OrgAddress, time.Now()); err != nil {
			log.Warnw("could not stamp organization branding-paid",
				"processId", c.vp.ID.Hex(), "orgAddress", c.vp.OrgAddress.String(), "error", err)
		}
	}
	return dueCents, nil
}

// walletMayRecord reports whether SetProcessPaymentPaidByWallet may write over payment: an
// abandoned or unpaid checkout, or a payment the wallet itself made that no delete is refunding.
// Anything else belongs to a card checkout or to a refund, and debiting the wallet for it would
// take money nothing records.
func walletMayRecord(payment *db.ProcessPayment) bool {
	switch payment.Status {
	case db.ProcessPaymentPending, db.ProcessPaymentFailed:
		return true
	case db.ProcessPaymentPaid:
		return payment.CheckoutSessionID == "" && payment.RefundWithheldCents == nil
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
	// census behind the draft can have grown in between (POST /census/{id} has no payment
	// state to consult), so publishing on the strength of the paid status alone would put a
	// bigger election on chain at the smaller price.
	due, err := a.paymentDueForPublish(vp)
	if errors.Is(err, errors.ErrPaymentSessionConflict) {
		log.Warnw("paid process payment refuses publication, staying paid for a manual publish",
			"processId", processID.Hex(), "error", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to re-price paid process: %w", err)
	}
	if due != nil {
		log.Warnw("paid process is now priced above its payment, staying paid for a manual publish",
			"processId", processID.Hex(), "paidCents", payment.AmountCents, "quotedCents", due.TotalCents)
		return nil
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
