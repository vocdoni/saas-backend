package api

import (
	"fmt"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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
// org is vp's organization, or nil to load it.
func (a *API) paidProcessProjection(
	vp *db.VotingProcess, census *db.Census, org *db.Organization,
) (p processCensusProjection, projected bool, err error) {
	if org == nil || org.Address != vp.OrgAddress {
		if org, err = a.db.Organization(vp.OrgAddress); err != nil {
			return p, false, errors.ErrGenericInternalServerError.WithErr(err)
		}
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
// check, not a size check: the formula rounds to €5, so a few extra voters usually cost nothing
// and are let through. A payment state it cannot read refuses too: this is the only guard
// between published elections and free growth, on the census routes and — through
// preflightCensusGrowth — on the memberbase changes that propagate into censuses.
//
// The 402 carries what the growth costs, which POST /processes/{processId}/census/checkout
// sells — with a card, or from the integrator wallet for a managed organization. Nothing is
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
