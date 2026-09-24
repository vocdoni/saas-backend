package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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

// paymentGateway is what the pay-per-process handlers need from Stripe, defined on the
// consumer side so API tests can install a fake; *stripe.Service satisfies it.
type paymentGateway interface {
	CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error)
	GetPaymentSession(sessionID string) (*stripe.PaymentSessionInfo, error)
	ExpirePaymentSession(sessionID string) error
	RefundProcessPayment(processID bson.ObjectID, paymentIntentID string) (*stripe.RefundInfo, error)
}

// processQuoteInput derives the pricing input from the draft state: census size, the 2FA
// channels the census authenticates with, the selected add-ons (branding only while the
// organization has not paid it yet), and the payer type (managed org -> integrator).
// Branding is settled in full by processQuote, which also honours the claim another process
// may hold on the organization's add-on — this function sees only the draft.
func processQuoteInput(vp *db.VotingProcess, census *db.Census, org *db.Organization) pricing.QuoteInput {
	payer := pricing.PayerStandard
	if org.ManagedBy != (common.Address{}) {
		payer = pricing.PayerIntegrator
	}
	return pricing.QuoteInput{
		CensusSize: int(census.Size),
		EmailTwoFA: census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldEmail),
		SMSTwoFA:   census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone),
		SignedCert: vp.AddOns.SignedCertificate,
		CustomURL:  vp.AddOns.CustomURL,
		Branding:   vp.AddOns.Branding && org.BrandingPaidAt.IsZero(),
		Payer:      payer,
	}
}

// processQuote loads everything the price depends on and computes the quote.
func (a *API) processQuote(vp *db.VotingProcess) (pricing.Quote, pricing.QuoteInput, error) {
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	input := processQuoteInput(vp, census, org)
	if input.Branding {
		// Quoting only reads the claim: a price query must never take one, or browsing the
		// price of a branded draft would deny branding to its siblings.
		claimable, _, err := a.brandingClaimable(vp, org)
		if err != nil {
			return pricing.Quote{}, pricing.QuoteInput{}, err
		}
		input.Branding = claimable
	}
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, input, nil
}

// BrandingClaimStaleAfter bounds how long a branding claim may be held by a process that
// has no payment record yet — the window between winning the claim and storing the payment,
// one Stripe round trip wide — before a sibling draft may take it over. A claim whose
// payment explicitly failed is releasable at once and does not wait for this. It is a var so
// tests can shorten it.
var BrandingClaimStaleAfter = 2 * time.Minute

// brandingClaimable reports whether vp may still carry the once-per-organization branding
// add-on, together with the claim it observed while deciding (zero when there is none), which
// ClaimOrganizationBranding needs to CAS against.
//
// The claim is a hint whose validity is re-derived from the claimant's payment, so it
// self-heals: a claim released by a failed payment, or abandoned before any payment was
// stored, is taken over by the next draft instead of denying branding to the organization
// forever.
func (a *API) brandingClaimable(vp *db.VotingProcess, org *db.Organization) (bool, bson.ObjectID, error) {
	claimant := org.BrandingClaimedBy
	if claimant == bson.NilObjectID || claimant == vp.ID {
		return true, claimant, nil
	}
	payment, err := a.db.ProcessPayment(claimant)
	switch {
	case err == db.ErrNotFound:
		// claimed but nothing stored yet: either a checkout mid-flight (leave it alone) or a
		// claim whose draft never got that far (take it over once the window has passed)
		return time.Since(org.BrandingClaimedAt) > BrandingClaimStaleAfter, claimant, nil
	case err != nil:
		// never assume a claim is free because it could not be read: that charges branding twice
		return false, claimant, errors.ErrGenericInternalServerError.WithErr(err)
	case payment.Status == db.ProcessPaymentFailed:
		return true, claimant, nil
	default:
		// pending, processing or paid: the claimant is still paying for branding
		return false, claimant, nil
	}
}

// claimBrandingForPayment wins the organization's branding claim for vp and returns the
// quote that must actually be charged: the one input asks for when the claim is ours, or a
// re-priced one without branding when another process holds it. The paying paths call this
// between quoting and charging, so two concurrent publishes can never both be billed for the
// once-per-organization add-on. input is updated to match the returned quote, because it is
// what the payment record and the quote hash are built from.
func (a *API) claimBrandingForPayment(
	vp *db.VotingProcess, org *db.Organization, input *pricing.QuoteInput,
) (pricing.Quote, error) {
	if input.Branding {
		won := false
		claimable, observed, err := a.brandingClaimable(vp, org)
		if err != nil {
			return pricing.Quote{}, err
		}
		if claimable {
			if won, err = a.db.ClaimOrganizationBranding(vp.OrgAddress, vp.ID, observed); err != nil {
				return pricing.Quote{}, errors.ErrGenericInternalServerError.WithErr(err)
			}
		}
		if !won {
			log.Infow("branding add-on is claimed by another process, pricing without it",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "claimedBy", observed.Hex())
			input.Branding = false
		}
	}
	quote, err := pricing.Compute(*input)
	if err != nil {
		return pricing.Quote{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, nil
}

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

// refusePaymentLocked refuses draft mutations while a payment is processing: money is in
// flight and its outcome is unknown, so the priced inputs must not move under it. A paid
// draft is not locked — every publish path re-prices against what was paid, refusing when
// the draft grew worth more, so an edit can only fix a draft that failed publish preflight,
// never under-charge. Pending payments do not lock either: the open session is expired and
// replaced at the next checkout. A paid draft is not refused by DELETE either: the money is
// returned first (see refundPaidProcess). Callers naming extra statuses in alsoLocked refuse
// those too.
// A payment state it cannot read is refused rather than assumed absent: nothing further
// down the write path consults payment state, so failing open here would let a transient
// Mongo error unlock exactly the draft this guard exists to protect.
func (a *API) refusePaymentLocked(w http.ResponseWriter, oid bson.ObjectID, alsoLocked ...db.ProcessPaymentStatus) bool {
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if err == db.ErrNotFound {
			return false // no payment: nothing to lock
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if payment.Status == db.ProcessPaymentProcessing {
		errors.ErrPaymentSessionConflict.
			Withf("the draft is locked while its payment is being processed").Write(w)
		return true
	}
	if slices.Contains(alsoLocked, payment.Status) {
		errors.ErrPaymentSessionConflict.
			Withf("the draft payment is %s; edit the draft and publish it instead", payment.Status).Write(w)
		return true
	}
	return false
}

// quoteProcessAtCensusSize prices a paid process as if its census held size members. It
// prices the branding add-on as the payment priced it, not as the draft reads now:
// BrandingPaidAt is stamped at fulfillment, so a fresh quote drops branding right after the
// payment that charged it — comparing that against the paid amount would hand the €149 back
// as free census growth.
func quoteProcessAtCensusSize(p processCensusProjection, size int64) (pricing.Quote, error) {
	grown := *p.census
	grown.Size = size
	input := processQuoteInput(p.vp, &grown, p.org)
	input.Branding = p.payment.Branding
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, nil
}

// processCensusProjection is everything pricing a paid process at a census size other than
// its current one depends on.
type processCensusProjection struct {
	vp      *db.VotingProcess
	census  *db.Census
	org     *db.Organization
	payment *db.ProcessPayment
}

// paidProcessProjection loads that state for vp, reporting ok=false with the failure already
// written. projected is false when there is nothing to project against — the process is free,
// was never quoted, or its payment is not paid — in which case no size check applies.
func (a *API) paidProcessProjection(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census,
) (p processCensusProjection, projected, ok bool) {
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil {
		if err == db.ErrNotFound {
			return p, false, true // free process, or never quoted
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return p, false, false
	}
	if payment.Status != db.ProcessPaymentPaid {
		return p, false, true
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return p, false, false
	}
	return processCensusProjection{vp: vp, census: census, org: org, payment: payment}, true, true
}

// refuseCensusGrowthBeyondPayment refuses growing a paid process's census past the price that
// was paid for it. It is a price check, not a size check: the formula rounds to €5, so a few
// extra voters usually cost nothing and are let through. It projects the size the census would
// actually reach — only the memberIDs that are not participants yet — so re-sending a batch
// that already landed is not refused as growth it is not. A payment state it cannot read
// refuses too: this is the only guard between POST-published elections and free growth.
//
// The 402 carries what the growth costs, which POST /processes/{processId}/census/checkout
// sells — with a card, or from the integrator wallet for a managed organization. Nothing is
// charged here: an id naming no member still counts as growth, so the projection can be a
// little high, and taking money against it would buy headroom nobody uses.
func (a *API) refuseCensusGrowthBeyondPayment(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census, memberIDs []string,
) bool {
	p, projected, ok := a.paidProcessProjection(w, vp, census)
	if !ok {
		return true
	}
	if !projected {
		return false
	}
	growth, err := a.db.CountNewCensusParticipants(census.ID.Hex(), memberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if growth <= 0 {
		return false
	}
	size := census.Size + growth
	quote, err := quoteProcessAtCensusSize(p, size)
	if err != nil {
		writeSubscriptionError(w, err)
		return true
	}
	if quote.TotalCents <= p.payment.AmountCents {
		return false
	}
	// carry the projection so the client sees what the growth costs, not a flat refusal
	errors.ErrPaymentRequired.
		Withf("the census cannot grow beyond the size the process was paid for; " +
			"buy the difference with POST /processes/{processId}/census/checkout").
		WithData(apicommon.ProcessCensusGrowthQuote{
			Lines:      quote.Lines,
			TotalCents: quote.TotalCents,
			PaidCents:  p.payment.AmountCents,
			DueCents:   quote.TotalCents - p.payment.AmountCents,
			CensusSize: size,
			Currency:   "eur",
		}).Write(w)
	return true
}

// createProcessCensusCheckoutHandler godoc
//
//	@Summary		Buy census headroom for a paid process
//	@Description	Opens a one-time Stripe checkout for the difference between what the process
//	@Description	already paid and what its census would cost at censusSize, so a census that
//	@Description	outgrew its price can be grown (or a paid draft republished) instead of being
//	@Description	stuck behind a 402. Add-ons are priced exactly as the original payment priced
//	@Description	them, so the branding add-on is never charged twice. The paid amount is raised
//	@Description	when the payment is verified by webhook — never before, so the census stays
//	@Description	refused until the money lands. A managed organization pays from its integrator
//	@Description	wallet instead: the difference is debited synchronously and the response carries
//	@Description	no checkout session, so the census can grow immediately. 400 when the process has
//	@Description	no completed payment, or when the target does not cost more than was already
//	@Description	paid. Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.ProcessCensusCheckoutRequest	true	"Census headroom to buy"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"No completed payment, or nothing left to buy"
//	@Failure		402			{object}	errors.Error	"Integrator wallet does not cover the difference"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/census/checkout [post]
func (a *API) createProcessCensusCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.ProcessCensusCheckoutRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.CensusSize < 1 {
		errors.ErrMalformedBody.Withf("censusSize must be a positive integer").Write(w)
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	p, projected, ok := a.paidProcessProjection(w, vp, census)
	if !ok {
		return
	}
	if !projected {
		errors.ErrMalformedBody.
			Withf("process has no completed payment to extend; pay it through POST /processes/{processId}/checkout").
			Write(w)
		return
	}
	quote, err := quoteProcessAtCensusSize(p, req.CensusSize)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if quote.QuoteRequired {
		errors.ErrQuoteRequired.Write(w)
		return
	}
	dueCents := quote.TotalCents - p.payment.AmountCents
	if dueCents <= 0 {
		errors.ErrMalformedBody.Withf("a census of %d is already covered by the %d eur cents paid",
			req.CensusSize, p.payment.AmountCents).Write(w)
		return
	}
	// A managed organization has no card: the difference comes out of its integrator's
	// prepaid wallet, the same debit a re-publish would have done, and takes effect at once.
	// The caller named the target size, so — unlike the growth guard, which can only project
	// an upper bound — this charges for headroom that was actually asked for.
	if p.org.ManagedBy != (common.Address{}) {
		if err := a.chargeManagedProcessWallet(managedProcessCharge{
			vp: vp, org: p.org, quote: quote, paid: p.payment, branding: p.payment.Branding,
		}); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		log.Infow("census headroom debited from the integrator wallet", "processId", oid.Hex(),
			"censusSize", req.CensusSize, "paidCents", p.payment.AmountCents, "targetCents", quote.TotalCents)
		apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
			AmountCents: dueCents,
			Currency:    "eur",
		})
		return
	}
	// One line for the difference, not the re-priced breakdown: the customer is buying the
	// increase, and billing them the full new total would charge the base price twice.
	session, err := a.paymentGW.CreatePaymentSession(&stripe.PaymentSessionParams{
		LineItems: []stripe.PaymentLineItem{{
			Description: fmt.Sprintf("census headroom up to %d voters", req.CensusSize),
			AmountCents: dueCents,
		}},
		Metadata: map[string]string{
			stripe.MetadataKeyProcessID:         oid.Hex(),
			stripe.MetadataKeyProcessTopUpCents: strconv.FormatInt(quote.TotalCents, 10),
			stripe.MetadataKeyRequestedBy:       user.Email,
		},
		OrgAddress:    vp.OrgAddress.String(),
		CustomerEmail: user.Email,
		ReturnURL:     req.ReturnURL,
		Locale:        req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create census top-up checkout session").WithErr(err).Write(w)
		return
	}
	// Nothing is stored: the payment record keeps its own session, and the envelope is
	// raised by the webhook against the *target* amount carried in the session metadata. An
	// abandoned top-up therefore leaves no state to reconcile, and two of them racing both
	// raise to their own target — monotonically, so the larger wins.
	log.Infow("census headroom checkout opened", "processId", oid.Hex(), "censusSize", req.CensusSize,
		"paidCents", p.payment.AmountCents, "targetCents", quote.TotalCents, "sessionId", session.ID)
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  dueCents,
		Currency:     "eur",
	})
}

// pricingHandler godoc
//
//	@Summary		Compute a pay-per-process price
//	@Description	Public calculator over the published pricing formula: pass the inputs, get the
//	@Description	same breakdown GET /processes/{processId}/price returns for a draft. EUR cents,
//	@Description	VAT excluded (Stripe Tax adds VAT at checkout). No organization context: branding
//	@Description	is charged as requested and legacy plan credits are not applied. Above 15 000
//	@Description	voters a custom quote is recommended; above 50 000 the flag is informational here —
//	@Description	self-service checkout enforces the block.
//	@Tags			processes
//	@Produce		json
//	@Param			voters				query		int		true	"Eligible voters (census size), at least 1"
//	@Param			emailTwoFA			query		bool	false	"Email 2FA add-on"
//	@Param			smsTwoFA			query		bool	false	"SMS 2FA add-on"
//	@Param			signedCertificate	query		bool	false	"Signed results certificate add-on"
//	@Param			customUrl			query		bool	false	"Custom URL add-on"
//	@Param			branding			query		bool	false	"Branding / white label add-on"
//	@Success		200					{object}	apicommon.ProcessPriceResponse
//	@Failure		400					{object}	errors.Error	"Missing or invalid voters"
//	@Router			/pricing [get]
func (*API) pricingHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	voters, err := strconv.Atoi(query.Get("voters"))
	if err != nil || voters < 1 {
		errors.ErrMalformedURLParam.Withf("voters must be a positive integer").Write(w)
		return
	}
	boolParam := func(name string) bool {
		value, _ := strconv.ParseBool(query.Get(name))
		return value
	}
	quote, err := pricing.Compute(pricing.QuoteInput{
		CensusSize: voters,
		EmailTwoFA: boolParam("emailTwoFA"),
		SMSTwoFA:   boolParam("smsTwoFA"),
		SignedCert: boolParam("signedCertificate"),
		CustomURL:  boolParam("customUrl"),
		Branding:   boolParam("branding"),
	})
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessPriceResponse{
		Lines:            quote.Lines,
		TotalCents:       quote.TotalCents,
		Currency:         "eur",
		QuoteRecommended: quote.QuoteRecommended,
		QuoteRequired:    quote.QuoteRequired,
	})
}

// processPriceHandler godoc
//
//	@Summary		Get the price of a voting process
//	@Description	Server-side price of the draft in EUR cents, VAT excluded: base price from the
//	@Description	census size plus the selected add-ons, with the current payment status. Prices
//	@Description	above 15 000 voters recommend a custom quote; above 50 000 self-service checkout
//	@Description	is unavailable. Requires Manager/Admin of the organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessPriceResponse
//	@Failure		400			{object}	errors.Error	"Census is empty or invalid"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Router			/processes/{processId}/price [get]
func (a *API) processPriceHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	quote, _, err := a.processQuote(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	resp := &apicommon.ProcessPriceResponse{
		Lines:            quote.Lines,
		TotalCents:       quote.TotalCents,
		Currency:         "eur",
		QuoteRecommended: quote.QuoteRecommended,
		QuoteRequired:    quote.QuoteRequired,
	}
	if payment, err := a.db.ProcessPayment(oid); err == nil {
		resp.PaymentStatus = payment.Status
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// createProcessCheckoutHandler godoc
//
//	@Summary		Start (or resume) the checkout of a voting process
//	@Description	Opens a one-time Stripe checkout session for the draft's server-calculated price
//	@Description	(VAT added by Stripe Tax at checkout) and returns its client secret. An open
//	@Description	session for the same price is reused; an obsolete one (the draft changed) is
//	@Description	expired and replaced. 409 (40177) while a payment is processing or completed —
//	@Description	a process is never charged twice. 422 (40176) above 50 000 voters (quote-only).
//	@Description	Free processes have nothing to pay: publish directly. Managed organizations pay
//	@Description	from their integrator's wallet at publish time, not through checkout.
//	@Description	Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Process ID"
//	@Param			request		body		apicommon.ProcessCheckoutRequest	true	"Checkout parameters"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"Free process, empty census or managed organization"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Payment already processing or completed"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/checkout [post]
func (a *API) createProcessCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.ProcessCheckoutRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	if vp.Published {
		errors.ErrDuplicateConflict.Withf("process already published").Write(w)
		return
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if org.ManagedBy != (common.Address{}) {
		errors.ErrMalformedBody.
			Withf("managed organization processes are paid from the integrator wallet at publish time").Write(w)
		return
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// win the branding claim before the session is priced, so the amount the customer is
	// asked to pay is the amount this process is entitled to charge
	if quote, err = a.claimBrandingForPayment(vp, org, &input); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if quote.QuoteRequired {
		errors.ErrQuoteRequired.Write(w)
		return
	}
	if quote.TotalCents == 0 {
		errors.ErrMalformedBody.Withf("process is free, publish it directly").Write(w)
		return
	}
	quoteHash := pricing.QuoteHash(input, quote.TotalCents)

	// reconcile with any existing payment before opening a session: never a second
	// charge for a paid or processing payment, reuse an open session whose price still
	// matches, expire an obsolete one before replacing it
	previousSessionID := ""
	if payment, err := a.db.ProcessPayment(oid); err == nil {
		switch payment.Status {
		case db.ProcessPaymentPending:
			session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID)
			if err != nil {
				errors.ErrStripeError.Withf("cannot reconcile checkout session").WithErr(err).Write(w)
				return
			}
			if session.Status == stripe.SessionStatusComplete {
				// the customer finished checkout and the webhook has not landed yet:
				// surface the in-flight payment instead of opening a second charge
				errors.ErrPaymentSessionConflict.Write(w)
				return
			}
			if session.Status == stripe.SessionStatusOpen {
				if payment.QuoteHash == quoteHash {
					apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
						ClientSecret: session.ClientSecret,
						SessionID:    session.ID,
						AmountCents:  payment.AmountCents,
						Currency:     payment.Currency,
					})
					return
				}
				// obsolete session: it must be gone before a replacement may exist,
				// so a failure to expire refuses the checkout rather than risking
				// two payable sessions
				if err := a.paymentGW.ExpirePaymentSession(session.ID); err != nil {
					errors.ErrStripeError.Withf("cannot expire obsolete checkout session").WithErr(err).Write(w)
					return
				}
			}
			previousSessionID = payment.CheckoutSessionID
		case db.ProcessPaymentFailed:
			previousSessionID = payment.CheckoutSessionID
		default:
			// processing, paid, or an unknown stored status: fail closed rather than
			// risking a second charge
			errors.ErrPaymentSessionConflict.Write(w)
			return
		}
	}

	lineItems := make([]stripe.PaymentLineItem, 0, len(quote.Lines))
	for _, line := range quote.Lines {
		if line.AmountCents == 0 {
			continue // a free base line (small census with paid add-ons) is not billable
		}
		lineItems = append(lineItems, stripe.PaymentLineItem{
			Description: line.Description,
			AmountCents: line.AmountCents,
		})
	}
	session, err := a.paymentGW.CreatePaymentSession(&stripe.PaymentSessionParams{
		LineItems: lineItems,
		Metadata: map[string]string{
			stripe.MetadataKeyProcessID:   oid.Hex(),
			stripe.MetadataKeyRequestedBy: user.Email,
		},
		OrgAddress:    vp.OrgAddress.String(),
		CustomerEmail: user.Email,
		ReturnURL:     req.ReturnURL,
		Locale:        req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create checkout session").WithErr(err).Write(w)
		return
	}
	stored, err := a.db.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        vp.OrgAddress,
		CheckoutSessionID: session.ID,
		QuoteHash:         quoteHash,
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
		RequestedBy:       user.Email,
		Branding:          input.Branding,
	}, previousSessionID)
	if err != nil || !stored {
		// the session exists but nothing references it: expire it so it can never be
		// paid, then surface the refusal (a concurrent payment won the state)
		if e := a.paymentGW.ExpirePaymentSession(session.ID); e != nil {
			log.Warnw("could not expire orphaned checkout session", "sessionId", session.ID, "error", e)
		}
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		errors.ErrPaymentSessionConflict.Write(w)
		return
	}
	if previousSessionID != "" && previousSessionID != session.ID {
		log.Infow("replaced obsolete checkout session",
			"processId", oid.Hex(), "previousSessionId", previousSessionID, "sessionId", session.ID)
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  quote.TotalCents,
		Currency:     "eur",
	})
}

// processCheckoutStatusHandler godoc
//
//	@Summary		Get the payment status of a voting process
//	@Description	The stored payment state (pending, processing, failed, paid) combined with the
//	@Description	live Stripe session state when a checkout session exists. Payment truth comes
//	@Description	from webhooks — this endpoint is for polling the outcome, it never fulfills.
//	@Description	Requires Manager/Admin of the organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessPaymentStatusResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found, or it has no payment"
//	@Router			/processes/{processId}/checkout [get]
func (a *API) processCheckoutStatusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Withf("process has no payment").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.ProcessPaymentStatusResponse{
		Status:      payment.Status,
		AmountCents: payment.AmountCents,
		Currency:    payment.Currency,
	}
	if !payment.PaidAt.IsZero() {
		resp.PaidAt = payment.PaidAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if payment.CheckoutSessionID != "" && a.paymentGW != nil {
		if session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID); err == nil {
			resp.SessionStatus = string(session.Status)
			resp.SessionPaymentStatus = string(session.PaymentStatus)
		}
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// refundPaidProcess returns a paid draft's money the way it arrived and records that it did:
// the payment row survives as the audit trail of a process that no longer exists, which is
// why the caller must not delete it. The organization's branding add-on is released with the
// refund, so its next draft is quoted — and charged — for what the deleted one paid.
//
// Reports false with the response already written when the money could not be returned.
// Nothing is recorded then and the draft stays, so the delete can be retried; destroying a
// draft whose money is still with us is the one outcome this must never produce.
func (a *API) refundPaidProcess(w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment) bool {
	refundID, ok := a.returnProcessPayment(w, vp, payment)
	if !ok {
		return false
	}
	if _, err := a.db.MarkProcessPaymentRefunded(vp.ID, refundID); err != nil {
		// the money is already back; losing the status write would let a retry refund
		// twice, so this is reported rather than swallowed
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return false
	}
	if payment.Branding {
		if err := a.db.ReleaseOrganizationBranding(vp.OrgAddress, vp.ID); err != nil {
			log.Warnw("could not release organization branding after refund",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "error", err)
		}
	}
	log.Infow("paid draft refunded", "processId", vp.ID.Hex(), "amountCents", payment.AmountCents,
		"refundId", refundID, "wallet", payment.CheckoutSessionID == "")
	return true
}

// returnProcessPayment moves the money back and returns the id the refund is known by. A
// card payment is refunded against its payment intent, VAT included; a wallet-paid process
// credits the integrator wallet that was debited, keyed so a retried delete cannot credit
// twice. Both are keyed on the process id, which is unique to this refund because a process
// is paid for once.
func (a *API) returnProcessPayment(
	w http.ResponseWriter, vp *db.VotingProcess, payment *db.ProcessPayment,
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
		if err := a.db.CreditWallet(db.WalletCredit{
			OrgAddress:     org.ManagedBy,
			AmountCents:    payment.AmountCents,
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
	refund, err := a.paymentGW.RefundProcessPayment(vp.ID, payment.PaymentIntentID)
	if err != nil {
		errors.ErrStripeError.Withf("could not refund the process payment").WithErr(err).Write(w)
		return "", false
	}
	return refund.ID, true
}
