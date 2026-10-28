package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

// quoteProcessAtCensusSize prices a paid process as if its census held size members. It
// prices the once-per-organization add-ons as its payments priced them, not as the draft reads
// now: the organization is stamped at fulfillment, so a fresh quote drops them right after the
// payment that charged them — comparing that against the paid amount would hand their price
// back as free census growth.
func quoteProcessAtCensusSize(p processCensusProjection, size int64) (pricing.Quote, error) {
	grown := *p.census
	grown.Size = size
	input := processQuoteInput(p.vp, &grown, p.org)
	input.Branding = p.paid.Charged(db.OrgAddOnBranding)
	input.CustomURL = p.paid.Charged(db.OrgAddOnCustomURL)
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, nil
}

// processCensusProjection is everything pricing a paid process at a census size other than
// its current one depends on.
type processCensusProjection struct {
	vp     *db.VotingProcess
	census *db.Census
	org    *db.Organization
	paid   processPaid
}

// paidProcessProjection loads that state for vp. projected is false when there is nothing to
// project against — a draft that paid nothing — in which case no size check applies. A published
// process always projects: it is given its non-wallet base when it has none. census must be vp's
// census as it stands, before any growth. org is vp's organization, or nil to load it.
func (a *API) paidProcessProjection(
	vp *db.VotingProcess, census *db.Census, org *db.Organization,
) (p processCensusProjection, projected bool, err error) {
	if org == nil || org.Address != vp.OrgAddress {
		if org, err = a.db.Organization(vp.OrgAddress); err != nil {
			return p, false, errors.ErrGenericInternalServerError.WithErr(err)
		}
	}
	paid, err := a.processPaidState(vp.ID, walletPayer(org))
	if err != nil {
		return p, false, errors.ErrGenericInternalServerError.WithErr(err)
	}
	// a published process with no base gets one: published before pay-per-process, or the
	// publish's own base write failed
	if vp.Published && !paid.hasBase() {
		if paid, err = a.grandfatherProcessPayment(processCensusProjection{vp: vp, census: census, org: org, paid: paid}); err != nil {
			return p, false, errors.ErrGenericInternalServerError.WithErr(err)
		}
	}
	if !paid.paid() {
		return p, false, nil // a draft: the publish gate prices it
	}
	return processCensusProjection{vp: vp, census: census, org: org, paid: paid}, true, nil
}

// grandfatherProcessPayment records the non-wallet base of a published process (p.vp, with p.census
// and p.org) that has none, and returns what it paid with it (p.paid, without the base). A process
// published before pay-per-process — published with no wallet movement and no base — gets the
// price it would have paid for its census as it stands: that much stays free, growth past it is
// charged like any other. One whose wallet paid for it (its publish's own base write failed) gets
// 0, since its ledger holds what it paid. It runs on the first growth check, which every census
// growth path goes through before writing, so the census it prices is the one the process was
// published with. The once-per-organization add-ons are left
// out, as the growth quote leaves out an unpaid add-on. A quote-only census gets a €0 base, which
// only records that it was published: censusGrowthPaymentError refuses every growth check of it.
func (a *API) grandfatherProcessPayment(p processCensusProjection) (processPaid, error) {
	vp, census, org, paid := p.vp, p.census, p.org, p.paid
	var baseCents pricing.Cents
	if paid.PaidCents == 0 {
		quote, err := quoteProcessAtCensusSize(processCensusProjection{
			vp: vp, census: census, org: org, paid: processPaid{ProcessLedgerState: &db.ProcessLedgerState{}},
		}, census.Size)
		if err != nil {
			return paid, err
		}
		baseCents = quote.TotalCents
	}
	if _, err := a.db.SetProcessPaymentBase(vp.ID, vp.OrgAddress, baseCents); err != nil {
		return paid, err
	}
	log.Infow("recorded the base of a published process", "processId", vp.ID.Hex(),
		"censusSize", census.Size, "baseCents", baseCents)
	// re-read rather than trust the write: a concurrent check may have written it first
	return a.processPaidState(vp.ID, paid.Payer)
}

// censusGrowthRefusal returns why adding memberIDs to a paid process's census is refused —
// growing it past the price that was paid for it — or nil. It projects the size the census
// would actually reach — only the memberIDs that are not participants yet — so re-sending a
// batch that already landed is not refused as growth it is not. See censusGrowthPaymentError
// for the check itself; write the result with writeSubscriptionError.
func (a *API) censusGrowthRefusal(vp *db.VotingProcess, census *db.Census, memberIDs []string) error {
	growth, err := a.db.CountNewCensusParticipants(census, memberIDs)
	if err != nil {
		return fmt.Errorf("failed to count new census participants: %w", err)
	}
	return a.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, by: growth})
}

// censusGrowth is a proposed growth of a process's census, as censusGrowthPaymentError prices it.
type censusGrowth struct {
	vp     *db.VotingProcess
	census *db.Census       // vp's census as it stands, before the growth
	org    *db.Organization // vp's organization, or nil to load it
	by     int64            // members added
	// size, when set, is the size to price instead of census.Size+by: what a resize pushes on
	// chain for one question, which for an eligibility subset is the subset, not the census
	size int64
}

// censusGrowthPaymentError returns the 402 for growing g.vp's census by g.by members when that
// would raise the price past what vp was paid, and nil when the growth fits. It is a price
// check, not a size check: growth fits while the paid amount (raised by headroom purchases)
// covers it, and a size that is quote-only is refused with ErrQuoteRequired (422) whatever was
// paid. A payment state it cannot read refuses too: this is the only guard
// between published elections and free growth, on the census routes and — through
// preflightCensusGrowth — on the memberbase changes that propagate into censuses.
//
// The 402 carries what the growth costs, which POST /processes/{processId}/census/checkout
// debits from the payer's wallet (after a top-up checkout when it falls short). Nothing is
// charged here: growth is an upper bound (an id naming no member, or a member already in the
// census, still counts), and taking money against it would buy headroom nobody uses.
func (a *API) censusGrowthPaymentError(g censusGrowth) error {
	vp, census := g.vp, g.census
	p, projected, err := a.paidProcessProjection(vp, census, g.org)
	if err != nil || !projected {
		return err
	}
	// no early exit at zero growth: the census may already have outgrown its price (grown by a
	// path this check does not guard), and the resizes that call this with zero growth push the
	// recounted size on chain even when nothing new is added
	size := census.Size + g.by
	if g.size > 0 {
		size = g.size
	}
	quote, err := quoteProcessAtCensusSize(p, size)
	if err != nil {
		return err
	}
	// quote-only has no price: its zero total is never "within what was paid"
	if quote.QuoteRequired {
		return errors.ErrQuoteRequired.Withf("the census of process %s would need a custom quote", vp.ID.Hex())
	}
	paidCents := p.paid.cents()
	if quote.TotalCents <= paidCents {
		return nil
	}
	// carry the projection so the client sees what the growth costs and what the wallet lacks
	wallet, err := a.db.Wallet(walletPayer(p.org))
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	dueCents := quote.TotalCents - paidCents
	// a managed organization has no checkout: census/checkout debits its integrator's wallet, and
	// when that falls short refuses with 40175, so the refusal here is that same 40175
	if p.org.ManagedBy != (common.Address{}) && wallet.BalanceCents < dueCents {
		return walletShortError(walletShort{org: p.org, quote: quote, dueCents: dueCents, balanceCents: wallet.BalanceCents})
	}
	return errors.ErrPaymentRequired.
		Withf("the census of process %s cannot grow beyond the size it was paid for; "+
			"buy the difference with POST /processes/{processId}/census/checkout", vp.ID.Hex()).
		WithData(apicommon.ProcessCensusGrowthQuote{
			ProcessID:      vp.ID.Hex(),
			Lines:          quote.Lines,
			TotalCents:     quote.TotalCents,
			PaidCents:      paidCents,
			DueCents:       dueCents,
			CensusSize:     size,
			Currency:       "eur",
			BalanceCents:   wallet.BalanceCents,
			ShortfallCents: max(dueCents-wallet.BalanceCents, 0),
		})
}

// createProcessCensusCheckoutHandler godoc
//
//	@Summary		Buy census headroom for a paid process
//	@Description	Debits the payer's wallet for the difference between what the process already paid
//	@Description	and what its census would cost at censusSize, so a census that outgrew its price can
//	@Description	be grown (or a paid draft republished) instead of being stuck behind a 402. Add-ons are
//	@Description	priced exactly as the original payment priced them, so a once-per-organization add-on
//	@Description	(branding, custom URL) is never charged twice. When the wallet covers it the paid
//	@Description	amount is raised at once and the response carries no checkout session (amountCents is
//	@Description	what was debited). When it does not: a managed organization gets 402 (40175); any other
//	@Description	gets a wallet top-up checkout for the shortfall (at least €1), and the headroom is
//	@Description	debited once that payment is verified by webhook — never before, so a published process's
//	@Description	census stays refused until then; if the process changed meanwhile the credit stays in the
//	@Description	wallet.
//	@Description	There is no status of its own for that checkout: poll GET /processes/{processId}/checkout,
//	@Description	whose amountCents reaches the new price once the headroom is debited, and the wallet
//	@Description	history (GET /organizations/{orgAddress}/wallet), which shows the topup credit and then a
//	@Description	payment row for the process. A topup with no payment row after it means the debit was
//	@Description	refused: for a published process, retry the growth and, on its 402 (shortfallCents 0),
//	@Description	call this endpoint again to debit the wallet; for a draft, publish it again: the publish
//	@Description	debits the difference from the wallet.
//	@Description	400 when the process has no completed payment, or when the target does not cost more
//	@Description	than was already paid. Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.ProcessCensusCheckoutRequest	true	"Census headroom to buy"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"No completed payment, or nothing left to buy"
//	@Failure		402			{object}	errors.Error	"Integrator wallet does not cover the difference (40175)"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Payment changed or is being refunded"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error
//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/processes/{processId}/census/checkout [post]
func (a *API) createProcessCensusCheckoutHandler(w http.ResponseWriter, r *http.Request) {
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
	if err := a.refuseRefundedDraft(vp); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	p, projected, err := a.paidProcessProjection(vp, census, nil)
	if err != nil {
		writeSubscriptionError(w, err)
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
	paidCents := p.paid.cents()
	dueCents := quote.TotalCents - paidCents
	if dueCents <= 0 {
		// a covered charge debits nothing, but redoes the stamps and the display cache a charge
		// cut short after its debit left (its own failure is the one already logged)
		if _, err := a.chargeProcessWallet(processCharge{vp: vp, org: p.org, quote: quote, fromCents: &paidCents}); err != nil {
			log.Debugw("covered census charge not refreshed", "processId", oid.Hex(), "error", err)
		}
		errors.ErrMalformedBody.Withf("a census of %d is already covered by the %d eur cents paid",
			req.CensusSize, paidCents).Write(w)
		return
	}
	// Every payer pays from its wallet, the same debit a re-publish would have done, and it takes
	// effect at once. The caller named the target size, so — unlike the growth guard, which can
	// only project an upper bound — this charges for headroom that was actually asked for. It is
	// priced from what the process paid as read above and applies only while that still holds, and
	// prices in no add-on of its own, so it never stamps a new one.
	debited, err := a.chargeProcessWallet(processCharge{vp: vp, org: p.org, quote: quote, fromCents: &paidCents})
	if err == nil {
		log.Infow("census headroom debited from the wallet", "processId", oid.Hex(),
			"censusSize", req.CensusSize, "paidCents", paidCents, "targetCents", quote.TotalCents,
			"debitedCents", debited)
		apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{AmountCents: debited, Currency: "eur"})
		return
	}
	shortfall, short := walletShortfall(err)
	if !short {
		// a managed organization's 40175, a payment that moved, or a failure
		writeSubscriptionError(w, err)
		return
	}
	// The top-up covers the shortfall (at least the minimum charge); the webhook credits it and
	// then debits the headroom, only while the process still paid what it was priced from. Nothing
	// is stored: no add-on is claimed, and an abandoned top-up leaves no state to reconcile. Only this
	// needs Stripe: a wallet that covers the headroom, or a managed organization, never gets here.
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	chargeCents := max(shortfall, pricing.MinimumChargeCents)
	session, err := a.openWalletTopUp(walletTopUp{
		wallet:      *p.org,
		user:        user,
		amountCents: chargeCents,
		description: fmt.Sprintf("wallet top-up for census headroom up to %d voters", req.CensusSize),
		metadata: map[string]string{
			stripe.MetadataKeyProcessID:             oid.Hex(),
			stripe.MetadataKeyProcessTopUpCents:     strconv.FormatInt(int64(quote.TotalCents), 10),
			stripe.MetadataKeyProcessTopUpFromCents: strconv.FormatInt(int64(paidCents), 10),
		},
		returnURL: req.ReturnURL,
		locale:    req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create census headroom top-up checkout").WithErr(err).Write(w)
		return
	}
	log.Infow("census headroom top-up checkout opened", "processId", oid.Hex(), "censusSize", req.CensusSize,
		"paidCents", paidCents, "targetCents", quote.TotalCents, "chargedCents", chargeCents,
		"sessionId", session.ID)
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  chargeCents,
		Currency:     "eur",
	})
}

// walletShortfall reports what an organization's wallet lacks when err is its 402 (40178,
// walletShortError); a managed organization's 40175 is not one: it cannot open a top-up.
func walletShortfall(err error) (pricing.Cents, bool) {
	var apiErr errors.Error
	if !errors.Is(err, errors.ErrPaymentRequired) || !errors.As(err, &apiErr) {
		return 0, false
	}
	due, ok := apiErr.Data.(*apicommon.ProcessPaymentRequired)
	if !ok {
		return 0, false
	}
	return due.ShortfallCents, true
}
