package api

import (
	"encoding/json"
	"maps"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
)

// walletTopUpMaxCents keeps a fat-fingered free-form top-up from going through unnoticed; the
// minimum is pricing.MinimumChargeCents, like every other charge.
const walletTopUpMaxCents = 100_000_00 // €100 000

// walletTopUp is one wallet top-up checkout: amountCents (net, VAT added by Stripe Tax) for the
// wallet of organization wallet, requested by user. metadata adds the process a top-up opened
// for a process pays.
type walletTopUp struct {
	wallet      db.Organization
	user        *db.User
	amountCents pricing.Cents
	description string
	metadata    map[string]string
	returnURL   string
	locale      string
}

// openWalletTopUp opens a top-up checkout. Every payment session the service opens goes through
// here, so every one carries the wallet it credits (MetadataKeyWalletTopUpOrg): the webhook routes
// on it, and a refund of its payment intent is traced back to it.
func (a *API) openWalletTopUp(t walletTopUp) (*stripe.PaymentSessionInfo, error) {
	metadata := map[string]string{
		stripe.MetadataKeyWalletTopUpOrg: t.wallet.Address.String(),
		stripe.MetadataKeyRequestedBy:    t.user.Email,
	}
	maps.Copy(metadata, t.metadata)
	return a.paymentGW.CreatePaymentSession(&stripe.PaymentSessionParams{
		LineItems:     []stripe.PaymentLineItem{{Description: t.description, AmountCents: t.amountCents}},
		Metadata:      metadata,
		OrgAddress:    t.wallet.Address.String(),
		CustomerEmail: t.user.Email,
		ReturnURL:     t.returnURL,
		Locale:        t.locale,
	})
}

// walletOrganization resolves the organization of a wallet route and authorizes the caller,
// writing the error and returning nil when refused. A managed organization has no wallet (its
// integrator pays). An API key acts as the user who created it, who may administer other
// organizations too, so it is pinned to its own organization here.
func (a *API) walletOrganization(w http.ResponseWriter, r *http.Request, roles ...db.UserRole) *db.Organization {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return nil
	}
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrOrganizationNotFound.Write(w)
		return nil
	}
	if key, isKey := apicommon.APIKeyFromContext(r.Context()); isKey && key.OrgAddress != org.Address {
		errors.ErrAPIKeyNotAllowed.Withf("an API key only reads its own organization's wallet").Write(w)
		return nil
	}
	authorized := false
	for _, role := range roles {
		authorized = authorized || user.HasRoleFor(org.Address, role)
	}
	if !authorized {
		errors.ErrUnauthorized.Withf("user lacks the role this wallet operation requires").Write(w)
		return nil
	}
	if org.ManagedBy != (common.Address{}) {
		errors.ErrManagedOrgHasNoWallet.Write(w)
		return nil
	}
	return org
}

// walletHandler godoc
//
//	@Summary		Get an organization's wallet
//	@Description	Prepaid EUR balance (1 credit = €1, VAT excluded) and paged history, newest first,
//	@Description	of an organization's wallet — a standard organization's or an integrator's. History
//	@Description	kinds: topup (Stripe checkout credit), payment (a process publish or census
//	@Description	headroom; processOrgAddress names the managed organization that spent it),
//	@Description	creditBack (a paid draft deleted, or a publish that failed for good) and refund (a
//	@Description	top-up refunded through Stripe). amountCents is signed. The balance is negative only
//	@Description	after a refund of credit already spent; the next top-up pays that first. 403 (40181)
//	@Description	for a managed organization, which has no wallet. Requires Admin or Manager.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `quota:read`) of this same organization.
//	@Tags			organizations
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Param			page		query		string	false	"Page number, starting at 1"
//	@Param			limit		query		string	false	"Items per page"
//	@Success		200			{object}	apicommon.WalletResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		403			{object}	errors.Error	"Managed organization, or an API key of another organization"
//	@Failure		404			{object}	errors.Error
//	@Failure		500			{object}	errors.Error
//	@Router			/organizations/{orgAddress}/wallet [get]
func (a *API) walletHandler(w http.ResponseWriter, r *http.Request) {
	org := a.walletOrganization(w, r, db.AdminRole, db.ManagerRole)
	if org == nil {
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	wallet, err := a.db.Wallet(org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	total, entries, err := a.db.WalletLedger(org.Address, params.Page, params.Limit)
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
		History:      entries,
		Pagination:   pagination,
	})
}

// createWalletTopUpHandler godoc
//
//	@Summary		Top up an organization's wallet
//	@Description	Opens a one-time Stripe checkout session that credits the organization's wallet once
//	@Description	the payment is verified by webhook — never before. The amount is EUR cents, VAT
//	@Description	excluded (Stripe Tax adds VAT at checkout), between €1 and €100 000. 403 (40181) for
//	@Description	a managed organization. Requires an Admin user session; not callable with an API key.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string							true	"Organization address"
//	@Param			request		body		apicommon.WalletTopUpRequest	true	"Top-up parameters"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"Amount out of bounds"
//	@Failure		401			{object}	errors.Error
//	@Failure		403			{object}	errors.Error	"Managed organization"
//	@Failure		404			{object}	errors.Error
//	@Failure		500			{object}	errors.Error
//	@Router			/organizations/{orgAddress}/wallet/topup [post]
func (a *API) createWalletTopUpHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	org := a.walletOrganization(w, r, db.AdminRole)
	if org == nil {
		return
	}
	user, _ := apicommon.UserFromContext(r.Context())
	req := &apicommon.WalletTopUpRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.AmountCents < pricing.MinimumChargeCents || req.AmountCents > walletTopUpMaxCents {
		errors.ErrMalformedBody.Withf("amount must be between %d and %d eur cents",
			pricing.MinimumChargeCents, walletTopUpMaxCents).Write(w)
		return
	}
	session, err := a.openWalletTopUp(walletTopUp{
		wallet:      *org,
		user:        user,
		amountCents: req.AmountCents,
		description: "wallet top-up",
		returnURL:   req.ReturnURL,
		locale:      req.Locale,
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
