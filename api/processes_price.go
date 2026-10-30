package api

import (
	"net/http"
	"strconv"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
)

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
