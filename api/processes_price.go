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
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	switch {
	case err == nil && payment.Status == db.ProcessPaymentPaid:
		// A paid process's branding is fixed by its payment, both ways. Paid for: it stays in
		// the price for good — fulfillment stamped the organization, which drops it from
		// processQuoteInput, and leaving it out would let a re-quote spend the €149 on voters.
		// Not paid for: it stays out, even if the claim has freed up since — nothing sells the
		// add-on to a paid process, so pricing it in would strand the draft behind a 402. The
		// update handler refuses the edit that would turn it on instead.
		input.Branding = payment.Branding
	case input.Branding:
		// Quoting only reads the claim: a price query must never take one, or browsing the
		// price of a branded draft would deny branding to its siblings.
		claimable, _, err := a.brandingClaimable(vp, org)
		if err != nil {
			return pricing.Quote{}, pricing.QuoteInput{}, err
		}
		input.Branding = claimable
	default:
	}
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, input, nil
}

// brandingClaimable reports whether vp may still carry the once-per-organization branding
// add-on, together with the claim it observed while deciding (zero when there is none), which
// ClaimOrganizationBranding needs to CAS against.
//
// The claim is a hint whose validity is re-derived from the claimant's payment, so it
// self-heals: a claim no longer backed by a branded payment — none stored, failed, refunded,
// or stored without branding because the claimant dropped the add-on — is taken over by the
// next draft instead of denying branding to the organization forever. Only once it is stale
// (db.BrandingClaimStaleAfter), though: a claimant retrying its payment refreshes the claim
// before storing the new payment, so a fresh claim may be backed by a payment not written yet.
func (a *API) brandingClaimable(vp *db.VotingProcess, org *db.Organization) (bool, bson.ObjectID, error) {
	claimant := org.BrandingClaimedBy
	if claimant == bson.NilObjectID || claimant == vp.ID {
		return true, claimant, nil
	}
	if time.Since(org.BrandingClaimedAt) <= db.BrandingClaimStaleAfter {
		return false, claimant, nil
	}
	payment, err := a.db.ProcessPayment(claimant)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return true, claimant, nil
	case err != nil:
		// never assume a claim is free because it could not be read: that charges branding twice
		return false, claimant, errors.ErrGenericInternalServerError.WithErr(err)
	case !payment.Branding,
		payment.Status == db.ProcessPaymentFailed,
		payment.Status == db.ProcessPaymentRefunded:
		return true, claimant, nil
	default:
		// a pending, processing or paid payment for branding: the claimant is still paying for it
		return false, claimant, nil
	}
}

// claimBrandingForPayment wins the organization's branding claim for vp and returns the
// quote that must actually be charged: the one input asks for when the claim is ours, or a
// re-priced one without branding when another process holds it. The paying paths call this
// between quoting and charging, so two concurrent publishes can never both be billed for the
// once-per-organization add-on. input is updated to match the returned quote, because it is
// what the payment record and the quote hash are built from.
//
// It also returns the claim this attempt took, zero unless it took one the draft did not hold
// before: a caller refused before any money moves hands it to releaseRefusedBrandingClaim, or
// its siblings would be priced without branding for nothing until the claim went stale. A claim
// the draft already held is never returned, since an earlier attempt's open payment may back it.
func (a *API) claimBrandingForPayment(
	vp *db.VotingProcess, org *db.Organization, input *pricing.QuoteInput,
) (pricing.Quote, time.Time, error) {
	var taken time.Time
	// with the organization already stamped, a branded input can only be this process's own
	// paid branding (processQuote): nothing left to claim
	if input.Branding && org.BrandingPaidAt.IsZero() {
		claimable, observed, err := a.brandingClaimable(vp, org)
		if err != nil {
			return pricing.Quote{}, time.Time{}, err
		}
		var claimedAt time.Time
		if claimable {
			if claimedAt, err = a.db.ClaimOrganizationBranding(vp.OrgAddress, vp.ID, observed); err != nil {
				return pricing.Quote{}, time.Time{}, errors.ErrGenericInternalServerError.WithErr(err)
			}
		}
		if claimedAt.IsZero() {
			log.Infow("branding add-on is claimed by another process, pricing without it",
				"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "claimedBy", observed.Hex())
			input.Branding = false
		} else if observed != vp.ID {
			taken = claimedAt
		}
	}
	quote, err := pricing.Compute(*input)
	if err != nil {
		return pricing.Quote{}, taken, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, taken, nil
}

// releaseRefusedBrandingClaim gives back the branding claim claimBrandingForPayment took for a
// paying attempt that was then refused before any money moved. A zero claimedAt took nothing.
// A failure only logs: the claim is unbacked, so it still frees itself once stale.
func (a *API) releaseRefusedBrandingClaim(vp *db.VotingProcess, claimedAt time.Time) {
	if claimedAt.IsZero() {
		return
	}
	if err := a.db.ReleaseOrganizationBrandingClaim(vp.OrgAddress, vp.ID, claimedAt); err != nil {
		log.Warnw("could not release the branding claim of a refused payment",
			"processId", vp.ID.Hex(), "orgAddress", vp.OrgAddress.String(), "error", err)
	}
}

// releaseDroppedCheckoutClaim frees the branding claim held for vp by a pending checkout that was
// just dropped (cancelled, or deleted with its draft). Otherwise the claim would keep a sibling
// draft from being charged branding for up to db.BrandingClaimStaleAfter. org is the
// organization as read before the checkout was dropped: the release is pinned to that claim, so a
// newer one, taken by a concurrent checkout of the same draft, is left alone.
func (a *API) releaseDroppedCheckoutClaim(vp *db.VotingProcess, org *db.Organization) {
	if org.BrandingClaimedBy == vp.ID {
		a.releaseRefusedBrandingClaim(vp, org.BrandingClaimedAt)
	}
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
