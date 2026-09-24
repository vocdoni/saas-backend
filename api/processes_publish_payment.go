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
// debited inside the publish path instead).
func (a *API) paymentDueForPublish(vp *db.VotingProcess) (*pricing.Quote, error) {
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
	if quote.TotalCents == 0 {
		return nil, nil
	}
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil {
		if err == db.ErrNotFound {
			return &quote, nil
		}
		return nil, fmt.Errorf("failed to get process payment: %w", err)
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
	if quote, err = a.claimBrandingForPayment(vp, org, &input); err != nil {
		return err
	}
	if quote.TotalCents == 0 {
		return nil
	}
	// What this process paid already, so the debit below is the delta. A payment state we
	// cannot read is never assumed absent: that would debit the whole price a second time.
	paid, err := a.db.ProcessPayment(vp.ID)
	if err != nil && err != db.ErrNotFound {
		return fmt.Errorf("failed to get process payment: %w", err)
	}
	return a.chargeManagedProcessWallet(managedProcessCharge{
		vp: vp, org: org, quote: quote, paid: paid, branding: input.Branding,
	})
}

// managedProcessCharge is one charge of a managed organization's process against its
// integrator's prepaid wallet: quote is the full price the process must end up having paid,
// paid the payment it already has (nil when it has none), and branding whether quote prices
// the once-per-organization add-on in.
type managedProcessCharge struct {
	vp       *db.VotingProcess
	org      *db.Organization
	quote    pricing.Quote
	paid     *db.ProcessPayment
	branding bool
}

// chargeManagedProcessWallet debits the integrator wallet for whatever of c.quote is not
// covered yet and records the new price, so both the publish path and a census that outgrew
// its price charge the same way: only the delta, at most once per (process, price). Returns a
// typed ErrInsufficientWalletBalance carrying the shortfall when the balance does not cover
// it, without touching the wallet.
func (a *API) chargeManagedProcessWallet(c managedProcessCharge) error {
	var alreadyCents int64
	if c.paid != nil && c.paid.Status == db.ProcessPaymentPaid {
		alreadyCents = c.paid.AmountCents
	}
	if alreadyCents >= c.quote.TotalCents {
		return nil // this price is already covered
	}
	dueCents := c.quote.TotalCents - alreadyCents
	if err := a.db.DebitWalletForProcess(db.WalletDebit{
		OrgAddress:  c.org.ManagedBy,
		ProcessID:   c.vp.ID,
		AmountCents: dueCents,
		PriceCents:  c.quote.TotalCents,
	}); err != nil {
		if err == db.ErrInsufficientWalletBalance {
			wallet, werr := a.db.Wallet(c.org.ManagedBy)
			if werr != nil {
				return errors.ErrInsufficientWalletBalance
			}
			return errors.ErrInsufficientWalletBalance.WithData(map[string]int64{
				"requiredCents":  dueCents,
				"availableCents": wallet.BalanceCents,
			})
		}
		return fmt.Errorf("failed to debit integrator wallet: %w", err)
	}
	walletPayment := &db.ProcessPayment{
		ProcessID:   c.vp.ID,
		OrgAddress:  c.vp.OrgAddress,
		AmountCents: c.quote.TotalCents,
		Currency:    "eur",
		Branding:    c.branding,
	}
	if c.paid != nil {
		// keep the record's history across a top-up
		walletPayment.CreatedAt = c.paid.CreatedAt
		walletPayment.RequestedBy = c.paid.RequestedBy
	}
	if _, err := a.db.SetProcessPaymentPaidByWallet(walletPayment); err != nil {
		log.Warnw("could not record wallet-paid process payment",
			"processId", c.vp.ID.Hex(), "error", err)
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
	return nil
}

// publishPaidProcess is the webhook fulfillment hook (stripe.Service.OnProcessPaid): a
// verified payment landed, publish the process as the user who requested the checkout.
// It fires only on the fulfillment that won the paid CAS, so it cannot double-publish;
// startProcessPublish's claim guards the race with a concurrent manual publish. Any
// refusal leaves the process paid — publishing later is free, never a second charge.
func (a *API) publishPaidProcess(processID bson.ObjectID) {
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		log.Warnw("paid process not found for publication", "processId", processID.Hex(), "error", err)
		return
	}
	if vp.Published {
		return
	}
	payment, err := a.db.ProcessPayment(processID)
	if err != nil {
		log.Warnw("paid process has no payment record", "processId", processID.Hex(), "error", err)
		return
	}
	user, err := a.db.UserByEmail(payment.RequestedBy)
	if err != nil {
		log.Warnw("paid process cannot auto-publish: requesting user not found, publish manually",
			"processId", processID.Hex(), "requestedBy", payment.RequestedBy, "error", err)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		log.Warnw("paid process census not found", "processId", processID.Hex(), "error", err)
		return
	}
	// Re-price before publishing. A Stripe retry can land days after the payment, and the
	// census behind the draft can have grown in between (POST /census/{id} has no payment
	// state to consult), so publishing on the strength of the paid status alone would put a
	// bigger election on chain at the smaller price.
	due, err := a.paymentDueForPublish(vp)
	if err != nil {
		log.Warnw("could not re-price paid process, staying paid for a manual publish",
			"processId", processID.Hex(), "error", err)
		return
	}
	if due != nil {
		log.Warnw("paid process is now priced above its payment, staying paid for a manual publish",
			"processId", processID.Hex(), "paidCents", payment.AmountCents, "quotedCents", due.TotalCents)
		return
	}
	if problems, _ := a.publishPreflightProblems(vp, questions, census, user); len(problems) > 0 {
		log.Warnw("paid process failed publish preflight, staying paid for a manual publish",
			"processId", processID.Hex(), "problems", strings.Join(problems, "; "))
		return
	}
	jobID, err := a.startProcessPublish(vp, questions, census, user)
	if err != nil {
		if err != errProcessAlreadyPublished {
			log.Warnw("could not start publication of paid process",
				"processId", processID.Hex(), "error", err)
		}
		return
	}
	log.Infow("paid process publication enqueued", "processId", processID.Hex(), "jobId", jobID)
}
