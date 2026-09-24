package api

import (
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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
	// no early exit at zero growth: the census may already have outgrown its price (a batch
	// that raced another past this check), and PUT /processes/{processId}/census pushes the
	// recounted size on chain even when nothing new is added
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
