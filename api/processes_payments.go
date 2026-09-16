package api

import (
	"encoding/json"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

// paymentGateway is what the pay-per-process handlers need from Stripe, defined on the
// consumer side so API tests can install a fake; *stripe.Service satisfies it.
type paymentGateway interface {
	CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error)
	GetPaymentSession(sessionID string) (*stripe.PaymentSessionInfo, error)
	ExpirePaymentSession(sessionID string) error
}

// processQuoteInput derives the pricing input from the draft state: census size, the 2FA
// channels the census authenticates with, the selected add-ons (branding only while the
// organization has not paid it yet), and the payer type (managed org -> integrator).
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
	quote, err := pricing.Compute(input)
	if err != nil {
		return pricing.Quote{}, pricing.QuoteInput{}, errors.ErrMalformedBody.WithErr(err)
	}
	return quote, input, nil
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

// createProcessCheckoutHandler godoc
//
//	@Summary		Start (or resume) the checkout of a voting process
//	@Description	Opens a one-time Stripe checkout session for the draft's server-calculated price
//	@Description	(VAT added by Stripe Tax at checkout) and returns its client secret. An open
//	@Description	session for the same price is reused; an obsolete one (the draft changed) is
//	@Description	expired and replaced. 409 (40177) while a payment is processing or completed —
//	@Description	a process is never charged twice. 422 (40176) above 50 000 voters (quote-only).
//	@Description	Free processes have nothing to pay: publish directly. Managed organizations pay
//	@Description	from their integrator's wallet at publish time, not through checkout.
//	@Description	Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Process ID"
//	@Param			request		body		apicommon.ProcessCheckoutRequest	true	"Checkout parameters"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"Free process, empty census or managed organization"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Payment already processing or completed"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/checkout [post]
func (a *API) createProcessCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.ProcessCheckoutRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
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
	if vp.Published {
		errors.ErrDuplicateConflict.Withf("process already published").Write(w)
		return
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if org.ManagedBy != (common.Address{}) {
		errors.ErrMalformedBody.
			Withf("managed organization processes are paid from the integrator wallet at publish time").Write(w)
		return
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if quote.QuoteRequired {
		errors.ErrQuoteRequired.Write(w)
		return
	}
	if quote.TotalCents == 0 {
		errors.ErrMalformedBody.Withf("process is free, publish it directly").Write(w)
		return
	}
	quoteHash := pricing.QuoteHash(input, quote.TotalCents)

	// reconcile with any existing payment before opening a session: never a second
	// charge for a paid or processing payment, reuse an open session whose price still
	// matches, expire an obsolete one before replacing it
	previousSessionID := ""
	if payment, err := a.db.ProcessPayment(oid); err == nil {
		switch payment.Status {
		case db.ProcessPaymentProcessing, db.ProcessPaymentPaid:
			errors.ErrPaymentSessionConflict.Write(w)
			return
		case db.ProcessPaymentPending:
			session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID)
			if err != nil {
				errors.ErrStripeError.Withf("cannot reconcile checkout session").WithErr(err).Write(w)
				return
			}
			if session.Status == "complete" {
				// the customer finished checkout and the webhook has not landed yet:
				// surface the in-flight payment instead of opening a second charge
				errors.ErrPaymentSessionConflict.Write(w)
				return
			}
			if session.Status == "open" {
				if payment.QuoteHash == quoteHash {
					apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
						ClientSecret: session.ClientSecret,
						SessionID:    session.ID,
						AmountCents:  payment.AmountCents,
						Currency:     payment.Currency,
					})
					return
				}
				// obsolete session: it must be gone before a replacement may exist,
				// so a failure to expire refuses the checkout rather than risking
				// two payable sessions
				if err := a.paymentGW.ExpirePaymentSession(session.ID); err != nil {
					errors.ErrStripeError.Withf("cannot expire obsolete checkout session").WithErr(err).Write(w)
					return
				}
			}
			previousSessionID = payment.CheckoutSessionID
		case db.ProcessPaymentFailed:
			previousSessionID = payment.CheckoutSessionID
		default:
			// unknown stored status: fail closed rather than risking a second charge
			errors.ErrPaymentSessionConflict.Write(w)
			return
		}
	}

	lineItems := make([]stripe.PaymentLineItem, 0, len(quote.Lines))
	for _, line := range quote.Lines {
		if line.AmountCents == 0 {
			continue // a free base line (small census with paid add-ons) is not billable
		}
		lineItems = append(lineItems, stripe.PaymentLineItem{
			Description: line.Description,
			AmountCents: line.AmountCents,
		})
	}
	session, err := a.paymentGW.CreatePaymentSession(&stripe.PaymentSessionParams{
		LineItems: lineItems,
		Metadata: map[string]string{
			stripe.MetadataKeyProcessID:   oid.Hex(),
			stripe.MetadataKeyRequestedBy: user.Email,
		},
		OrgAddress:    vp.OrgAddress.String(),
		CustomerEmail: user.Email,
		ReturnURL:     req.ReturnURL,
		Locale:        req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create checkout session").WithErr(err).Write(w)
		return
	}
	stored, err := a.db.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        vp.OrgAddress,
		CheckoutSessionID: session.ID,
		QuoteHash:         quoteHash,
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
		RequestedBy:       user.Email,
	})
	if err != nil || !stored {
		// the session exists but nothing references it: expire it so it can never be
		// paid, then surface the refusal (a concurrent payment won the state)
		if e := a.paymentGW.ExpirePaymentSession(session.ID); e != nil {
			log.Warnw("could not expire orphaned checkout session", "sessionId", session.ID, "error", e)
		}
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		errors.ErrPaymentSessionConflict.Write(w)
		return
	}
	if previousSessionID != "" && previousSessionID != session.ID {
		log.Infow("replaced obsolete checkout session",
			"processId", oid.Hex(), "previousSessionId", previousSessionID, "sessionId", session.ID)
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  quote.TotalCents,
		Currency:     "eur",
	})
}

// processCheckoutStatusHandler godoc
//
//	@Summary		Get the payment status of a voting process
//	@Description	The stored payment state (pending, processing, failed, paid) combined with the
//	@Description	live Stripe session state when a checkout session exists. Payment truth comes
//	@Description	from webhooks — this endpoint is for polling the outcome, it never fulfills.
//	@Description	Requires Manager/Admin of the organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessPaymentStatusResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found, or it has no payment"
//	@Router			/processes/{processId}/checkout [get]
func (a *API) processCheckoutStatusHandler(w http.ResponseWriter, r *http.Request) {
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
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Withf("process has no payment").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.ProcessPaymentStatusResponse{
		Status:      payment.Status,
		AmountCents: payment.AmountCents,
		Currency:    payment.Currency,
	}
	if !payment.PaidAt.IsZero() {
		resp.PaidAt = payment.PaidAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if payment.CheckoutSessionID != "" && a.paymentGW != nil {
		if session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID); err == nil {
			resp.SessionStatus = session.Status
			resp.SessionPaymentStatus = session.PaymentStatus
		}
	}
	apicommon.HTTPWriteJSON(w, resp)
}
