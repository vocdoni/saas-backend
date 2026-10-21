package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/stripe"
)

// Wallet top-up bounds in EUR cents: below the minimum a charge is not worth its fees;
// the maximum keeps a fat-fingered amount from going through unnoticed.
const (
	walletTopUpMinCents = 10_00      // €10
	walletTopUpMaxCents = 100_000_00 // €100 000
)

// walletHandler godoc
//
//	@Summary		Get the integrator wallet
//	@Description	Prepaid EUR balance and paged ledger (top-ups and per-process debits, newest
//	@Description	first) of the caller's integrator organization, resolved from the authenticated
//	@Description	principal (path-less, like the other integrator endpoints).
//	@Description
//	@Description	Also callable with a scoped API key (scope: `quota:read`).
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page	query		string	false	"Page number, starting at 1"
//	@Param			limit	query		string	false	"Items per page"
//	@Success		200		{object}	apicommon.WalletResponse
//	@Failure		401		{object}	errors.Error
//	@Failure		403		{object}	errors.Error	"Not an integrator"
//	@Failure		500		{object}	errors.Error
//	@Router			/wallet [get]
func (a *API) walletHandler(w http.ResponseWriter, r *http.Request) {
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get("page"), r.URL.Query().Get("limit"))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	wallet, err := a.db.Wallet(integratorAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	total, entries, err := a.db.WalletLedger(integratorAddr, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, total)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.WalletResponse{
		BalanceCents: wallet.BalanceCents,
		Currency:     "eur",
		Ledger:       entries,
		Pagination:   pagination,
	})
}

// createWalletTopUpHandler godoc
//
//	@Summary		Top up the integrator wallet
//	@Description	Opens a one-time Stripe checkout session that credits the caller's integrator
//	@Description	wallet once the payment is verified by webhook — never before. The amount is
//	@Description	EUR cents, VAT excluded (Stripe Tax adds VAT at checkout). Requires a user
//	@Description	session whose user is an Admin of the integrator organization; deliberately not
//	@Description	callable with an API key.
//	@Tags			integrator
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.WalletTopUpRequest	true	"Top-up parameters"
//	@Success		200		{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400		{object}	errors.Error	"Amount out of bounds"
//	@Failure		401		{object}	errors.Error
//	@Failure		403		{object}	errors.Error	"Not an integrator"
//	@Failure		500		{object}	errors.Error
//	@Router			/wallet/topup [post]
func (a *API) createWalletTopUpHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the integrator organization").Write(w)
		return
	}
	req := &apicommon.WalletTopUpRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.AmountCents < walletTopUpMinCents || req.AmountCents > walletTopUpMaxCents {
		errors.ErrMalformedBody.Withf("amount must be between %d and %d eur cents",
			walletTopUpMinCents, walletTopUpMaxCents).Write(w)
		return
	}
	session, err := a.paymentGW.CreatePaymentSession(&stripe.PaymentSessionParams{
		LineItems: []stripe.PaymentLineItem{
			{Description: "integrator wallet top-up", AmountCents: req.AmountCents},
		},
		Metadata: map[string]string{
			stripe.MetadataKeyWalletTopUpOrg: integratorAddr.String(),
			stripe.MetadataKeyRequestedBy:    user.Email,
		},
		OrgAddress:    integratorAddr.String(),
		CustomerEmail: user.Email,
		ReturnURL:     req.ReturnURL,
		Locale:        req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create top-up checkout session").WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  req.AmountCents,
		Currency:     "eur",
	})
}
