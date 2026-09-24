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

// paidProcessProjection loads that state for vp. projected is false when there is nothing to
// project against — a draft that was never quoted, or whose payment is not paid — in which
// case no size check applies. A published process always projects: it is given an envelope
// when it has none, and refused while its payment is in flight. census must be vp's census as it stands, before any growth.
func (a *API) paidProcessProjection(
	vp *db.VotingProcess, census *db.Census,
) (p processCensusProjection, projected bool, err error) {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return p, false, errors.ErrGenericInternalServerError.WithErr(err)
	}
	payment, err := a.db.ProcessPayment(vp.ID)
	// a published process with no envelope gets one: published before pay-per-process, or its
	// only checkout failed (the envelope replaces a failed payment, never a live one)
	missing := errors.Is(err, db.ErrNotFound) || (err == nil && payment.Status == db.ProcessPaymentFailed)
	if vp.Published && missing {
		payment, err = a.grandfatherProcessPayment(vp, census, org)
	}
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return p, false, nil // a draft never quoted
		}
		return p, false, errors.ErrGenericInternalServerError.WithErr(err)
	}
	if payment.Status != db.ProcessPaymentPaid {
		if vp.Published {
			// a payment still in flight on a published process is not "no limit": the census
			// waits for it to settle rather than growing past a price nobody paid
			return p, false, errors.ErrPaymentSessionConflict.Withf(
				"process %s has a %s payment; retry once it settles", vp.ID.Hex(), payment.Status)
		}
		return p, false, nil // a draft: the publish gate prices it
	}
	return processCensusProjection{vp: vp, census: census, org: org, payment: payment}, true, nil
}

// grandfatherProcessPayment gives a process published before pay-per-process — the only kind
// that is published with no payment, since publish records a €0 envelope for free ones — the
// envelope it would have paid for its census as it stands: that much stays free, growth past
// it is charged like any other. It runs on the first growth check, which every census growth
// path goes through before writing, so the census it prices is the one the process was
// published with. Branding is left out, as the growth quote leaves out an unpaid add-on.
func (a *API) grandfatherProcessPayment(
	vp *db.VotingProcess, census *db.Census, org *db.Organization,
) (*db.ProcessPayment, error) {
	quote, err := quoteProcessAtCensusSize(processCensusProjection{
		vp: vp, census: census, org: org, payment: &db.ProcessPayment{},
	}, census.Size)
	if err != nil {
		return nil, err
	}
	if _, err := a.db.SetProcessPaymentEnvelope(vp.ID, vp.OrgAddress, quote.TotalCents); err != nil {
		return nil, err
	}
	log.Infow("grandfathered the payment of a process published before pay-per-process",
		"processId", vp.ID.Hex(), "censusSize", census.Size, "amountCents", quote.TotalCents)
	// re-read rather than trust the insert: a concurrent check may have written it first
	return a.db.ProcessPayment(vp.ID)
}

// censusGrowthRefusal returns why adding memberIDs to a paid process's census is refused —
// growing it past the price that was paid for it — or nil. It projects the size the census
// would actually reach — only the memberIDs that are not participants yet — so re-sending a
// batch that already landed is not refused as growth it is not. See censusGrowthPaymentError
// for the check itself; write the result with writeSubscriptionError.
func (a *API) censusGrowthRefusal(vp *db.VotingProcess, census *db.Census, memberIDs []string) error {
	growth, err := a.db.CountNewCensusParticipants(census.ID.Hex(), memberIDs)
	if err != nil {
		return fmt.Errorf("failed to count new census participants: %w", err)
	}
	return a.censusGrowthPaymentError(vp, census, growth)
}

// censusGrowthPaymentError returns the 402 for growing vp's census by growth members when that
// would raise the price past what vp was paid, and nil when the growth fits. It is a price
// check, not a size check: the formula rounds to €5, so a few extra voters usually cost nothing
// and are let through. A payment state it cannot read refuses too: this is the only guard
// between published elections and free growth, on the census routes and — through
// preflightCensusGrowth — on the memberbase changes that propagate into censuses.
//
// The 402 carries what the growth costs, which POST /processes/{processId}/census/checkout
// sells — with a card, or from the integrator wallet for a managed organization. Nothing is
// charged here: growth is an upper bound (an id naming no member, or a member already in the
// census, still counts), and taking money against it would buy headroom nobody uses.
func (a *API) censusGrowthPaymentError(vp *db.VotingProcess, census *db.Census, growth int64) error {
	p, projected, err := a.paidProcessProjection(vp, census)
	if err != nil || !projected {
		return err
	}
	// no early exit at zero growth: the census may already have outgrown its price (a batch
	// that raced another past this check), and PUT /processes/{processId}/census pushes the
	// recounted size on chain even when nothing new is added
	size := census.Size + growth
	quote, err := quoteProcessAtCensusSize(p, size)
	if err != nil {
		return err
	}
	if quote.TotalCents <= p.payment.AmountCents {
		return nil
	}
	// carry the projection so the client sees what the growth costs, not a flat refusal
	return errors.ErrPaymentRequired.
		Withf("the census of process %s cannot grow beyond the size it was paid for; "+
			"buy the difference with POST /processes/{processId}/census/checkout", vp.ID.Hex()).
		WithData(apicommon.ProcessCensusGrowthQuote{
			ProcessID:  vp.ID.Hex(),
			Lines:      quote.Lines,
			TotalCents: quote.TotalCents,
			PaidCents:  p.payment.AmountCents,
			DueCents:   quote.TotalCents - p.payment.AmountCents,
			CensusSize: size,
			Currency:   "eur",
		})
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
//	@Failure		403			{object}	errors.Error	"API key used to buy an unmanaged organization's headroom by card"
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
	p, projected, err := a.paidProcessProjection(vp, census)
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
		// what was actually debited: a concurrent purchase may have covered part or all of it
		debited, err := a.chargeManagedProcessWallet(managedProcessCharge{
			vp: vp, org: p.org, quote: quote, branding: p.payment.Branding,
		})
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		log.Infow("census headroom debited from the integrator wallet", "processId", oid.Hex(),
			"censusSize", req.CensusSize, "paidCents", p.payment.AmountCents, "targetCents", quote.TotalCents,
			"debitedCents", debited)
		apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
			AmountCents: debited,
			Currency:    "eur",
		})
		return
	}
	// A card is the organization's own: API keys are allowed on this route for the integrator's
	// wallet branch above, never to open a card checkout (POST /processes/{id}/checkout is not
	// on the key allowlist either).
	if _, isKey := apicommon.APIKeyFromContext(r.Context()); isKey {
		errors.ErrAPIKeyNotAllowed.Withf("census headroom of an unmanaged organization is paid by card").Write(w)
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
			stripe.MetadataKeyProcessID:             oid.Hex(),
			stripe.MetadataKeyProcessTopUpCents:     strconv.FormatInt(quote.TotalCents, 10),
			stripe.MetadataKeyProcessTopUpFromCents: strconv.FormatInt(p.payment.AmountCents, 10),
			stripe.MetadataKeyRequestedBy:           user.Email,
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
	// raised by the webhook from the base to the target amount carried in the session metadata.
	// An abandoned top-up therefore leaves no state to reconcile. Of two opened against the same
	// base, the first paid raises it and the other is refunded: its difference was priced from
	// an amount the payment no longer has.
	log.Infow("census headroom checkout opened", "processId", oid.Hex(), "censusSize", req.CensusSize,
		"paidCents", p.payment.AmountCents, "targetCents", quote.TotalCents, "sessionId", session.ID)
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  dueCents,
		Currency:     "eur",
	})
}
