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
