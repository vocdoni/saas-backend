package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// processQuoteInput derives the pricing input from the draft state: census size, the 2FA
// channels the census authenticates with, the selected add-ons (each once-per-organization
// add-on only while the organization has not paid it yet), and the payer type (managed org ->
// integrator). The once-per-organization add-ons are settled in full by processQuote, which also
// honours the claim another process may hold on them — this function sees only the draft.
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
		CustomURL:  vp.AddOns.CustomURL && org.CustomURLPaidAt.IsZero(),
		Branding:   vp.AddOns.Branding && org.BrandingPaidAt.IsZero(),
		Payer:      payer,
	}
}

// quoteAddOn is the field of in that prices the once-per-organization addOn.
func quoteAddOn(in *pricing.QuoteInput, addOn db.OrgAddOn) *bool {
	if addOn == db.OrgAddOnCustomURL {
		return &in.CustomURL
	}
	return &in.Branding
}

// orgAddOnCents is the list price of a once-per-organization add-on.
func orgAddOnCents(addOn db.OrgAddOn) pricing.Cents {
	if addOn == db.OrgAddOnCustomURL {
		return pricing.CustomURLCents
	}
	return pricing.BrandingCents
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
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	paid := err == nil && payment.Status == db.ProcessPaymentPaid
	for _, addOn := range db.OrgAddOns {
		priced := quoteAddOn(&input, addOn)
		switch {
		case paid:
			// A paid process's once-per-organization add-ons are fixed by its payment, both
			// ways. Paid for: they stay in the price for good — fulfillment stamped the
			// organization, which drops them from processQuoteInput, and leaving them out would
			// let a re-quote spend their price on voters. Not paid for: they stay out, even if
			// the claim has freed up since — nothing sells an add-on to a paid process, so
			// pricing it in would strand the draft behind a 402. The update handler refuses
			// the edit that would turn one on instead.
			*priced = payment.Charged(addOn)
		case *priced:
			// Quoting only reads the claim: a price query must never take one, or browsing the
			// price of a draft would deny the add-on to its siblings.
			claimable, _, err := a.orgAddOnClaimable(vp, org, addOn)
			if err != nil {
				return pricing.Quote{}, pricing.QuoteInput{}, err
			}
			*priced = claimable
		default:
		}
	}
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, input, nil
}

// orgAddOnClaimable reports whether vp may still carry the once-per-organization addOn,
// together with the claim it observed while deciding (zero when there is none), which
// ClaimOrganizationAddOn needs to CAS against.
//
// The claim is a hint whose validity is re-derived from the claimant's payment, so it
// self-heals: a claim no longer backed by a payment charging the add-on — none stored, failed,
// refunded, or stored without it because the claimant dropped the add-on — is taken over by the
// next draft instead of denying the add-on to the organization forever. Only once it is stale
// (db.OrgAddOnClaimStaleAfter), though: a claimant retrying its payment refreshes the claim
// before storing the new payment, so a fresh claim may be backed by a payment not written yet.
func (a *API) orgAddOnClaimable(
	vp *db.VotingProcess, org *db.Organization, addOn db.OrgAddOn,
) (bool, bson.ObjectID, error) {
	claimant, claimedAt := org.AddOnClaim(addOn)
	if claimant == bson.NilObjectID || claimant == vp.ID {
		return true, claimant, nil
	}
	if time.Since(claimedAt) <= db.OrgAddOnClaimStaleAfter {
		return false, claimant, nil
	}
	payment, err := a.db.ProcessPayment(claimant)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return true, claimant, nil
	case err != nil:
		// never assume a claim is free because it could not be read: that charges the add-on twice
		return false, claimant, errors.ErrGenericInternalServerError.WithErr(err)
	case !payment.Charged(addOn),
		payment.Status == db.ProcessPaymentFailed,
		payment.Status == db.ProcessPaymentRefunded:
		return true, claimant, nil
	default:
		// a pending, processing or paid payment for the add-on: the claimant is still paying for it
		return false, claimant, nil
	}
}

// orgAddOnClaims are the claims a paying attempt took, by add-on: only those it did not hold
// before. A nil map took nothing.
type orgAddOnClaims map[db.OrgAddOn]time.Time

// claimOrgAddOnsForPayment wins the organization's claim on every once-per-organization add-on
// input prices for vp, and returns the quote that must actually be charged: the one input asks
// for when every claim is ours, or a re-priced one without each add-on another process holds.
// The paying paths call this between quoting and charging, so two concurrent publishes can never
// both be billed for one add-on. input is updated to match the returned quote, because it is what
// the payment record and the quote hash are built from.
//
// It also returns the claims this attempt took that the draft did not hold before: a caller
// refused before any money moves hands them to releaseRefusedOrgAddOnClaims, or its siblings
// would be priced without the add-ons for nothing until the claims went stale. A claim the draft
// already held is never returned, since an earlier attempt's open payment may back it.
func (a *API) claimOrgAddOnsForPayment(
	vp *db.VotingProcess, org *db.Organization, input *pricing.QuoteInput,
) (pricing.Quote, orgAddOnClaims, error) {
	var taken orgAddOnClaims
	for _, addOn := range db.OrgAddOns {
		priced := quoteAddOn(input, addOn)
		// with the organization already stamped, a priced add-on can only be this process's
		// own paid one (processQuote): nothing left to claim
		if !*priced || !org.AddOnPaidAt(addOn).IsZero() {
			continue
		}
		claimable, observed, err := a.orgAddOnClaimable(vp, org, addOn)
		if err != nil {
			return pricing.Quote{}, taken, err
		}
		var claimedAt time.Time
		if claimable {
			claimedAt, err = a.db.ClaimOrganizationAddOn(db.OrgAddOnClaim{
				OrgAddress: vp.OrgAddress, AddOn: addOn, ProcessID: vp.ID, Observed: observed,
			})
			if err != nil {
				return pricing.Quote{}, taken, errors.ErrGenericInternalServerError.WithErr(err)
			}
		}
		switch {
		case claimedAt.IsZero():
			log.Infow("once-per-organization add-on is claimed by another process, pricing without it",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "addOn", addOn,
				"claimedBy", observed.Hex())
			*priced = false
		case observed != vp.ID:
			if taken == nil {
				taken = orgAddOnClaims{}
			}
			taken[addOn] = claimedAt
		default:
		}
	}
	quote, err := pricing.Compute(*input)
	if err != nil {
		return pricing.Quote{}, taken, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, taken, nil
}

// releaseRefusedOrgAddOnClaims gives back the claims claimOrgAddOnsForPayment took for a paying
// attempt that was then refused before any money moved. A failure only logs: the claim is
// unbacked, so it still frees itself once stale.
func (a *API) releaseRefusedOrgAddOnClaims(vp *db.VotingProcess, claims orgAddOnClaims) {
	for addOn, claimedAt := range claims {
		if err := a.db.ReleaseOrganizationAddOnClaim(db.OrgAddOnClaim{
			OrgAddress: vp.OrgAddress, AddOn: addOn, ProcessID: vp.ID, ClaimedAt: claimedAt,
		}); err != nil {
			log.Warnw("could not release the add-on claim of a refused payment",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "addOn", addOn, "error", err)
		}
	}
}

// releaseDroppedCheckoutClaim frees the add-on claims held for vp by a pending checkout that was
// just dropped (cancelled, or deleted with its draft). Otherwise a claim would keep a sibling
// draft from being charged the add-on for up to db.OrgAddOnClaimStaleAfter. org is the
// organization as read before the checkout was dropped: each release is pinned to that claim, so
// a newer one, taken by a concurrent checkout of the same draft, is left alone.
func (a *API) releaseDroppedCheckoutClaim(vp *db.VotingProcess, org *db.Organization) {
	held := orgAddOnClaims{}
	for _, addOn := range db.OrgAddOns {
		if claimant, claimedAt := org.AddOnClaim(addOn); claimant == vp.ID && !claimedAt.IsZero() {
			held[addOn] = claimedAt
		}
	}
	a.releaseRefusedOrgAddOnClaims(vp, held)
}

// pricingHandler godoc
//
//	@Summary		Compute a pay-per-process price
//	@Description	Public calculator over the published pricing formula: pass the inputs, get the
//	@Description	same breakdown GET /processes/{processId}/price returns for a draft. EUR cents,
//	@Description	VAT excluded (Stripe Tax adds VAT at checkout). No organization context: branding
//	@Description	is charged as requested and legacy plan credits are not applied. Base price on
//	@Description	graduated bands, free up to 25 voters; add-ons are charged on free processes too.
//	@Description	A priced total below €1 is raised to €1 by a minimumCharge line. Above 15 000
//	@Description	voters a custom quote is recommended (quoteRecommended). Above 50 000 the process
//	@Description	is quote-only: quoteRequired is true and there is no price (empty lines,
//	@Description	totalCents 0 — not free).
//	@Tags			processes
//	@Produce		json
//	@Param			voters				query		int		true	"Eligible voters (census size), at least 1"
//	@Param			emailTwoFA			query		bool	false	"Email 2FA add-on (free for standard organizations)"
//	@Param			smsTwoFA			query		bool	false	"SMS 2FA add-on (€0.05 per voter)"
//	@Param			signedCertificate	query		bool	false	"Signed results certificate add-on (€39)"
//	@Param			customUrl			query		bool	false	"Custom URL add-on (€99, once per organization)"
//	@Param			branding			query		bool	false	"Branding / white label add-on (€149, once per organization)"
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
//	@Description	census size plus the selected add-ons, with the current payment status. Branding
//	@Description	and custom URL are once per organization: not priced once the organization paid
//	@Description	them, nor while another draft's payment holds them. Above 15 000 voters a custom
//	@Description	quote is recommended; above 50 000 the process is quote-only: no price (empty
//	@Description	lines, totalCents 0, quoteRequired true), and publish and checkout answer 422
//	@Description	(40176). Requires Manager/Admin of the organization.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
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
