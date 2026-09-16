package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// paymentDueForPublish decides whether publication must be refused for lack of payment.
// A non-nil quote is what is still owed (answer 402 with it). Nil means clear to
// publish: the process is free, already paid, or managed (its integrator wallet is
// debited inside the publish path instead).
func (a *API) paymentDueForPublish(vp *db.VotingProcess) (*pricing.Quote, error) {
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization: %w", err)
	}
	if org.ManagedBy != (common.Address{}) {
		return nil, nil
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		return nil, err
	}
	if quote.TotalCents == 0 {
		return nil, nil
	}
	payment, err := a.db.ProcessPayment(vp.ID)
	if err != nil {
		if err == db.ErrNotFound {
			return &quote, nil
		}
		return nil, fmt.Errorf("failed to get process payment: %w", err)
	}
	if payment.Status != db.ProcessPaymentPaid {
		return &quote, nil
	}
	// paid wins even when the draft no longer matches what was paid for: the money was
	// taken and paid is terminal, so refusing here could never be resolved by the user.
	// The edit guard makes this unreachable outside a tiny race; log it if it happens.
	if payment.QuoteHash != pricing.QuoteHash(input, quote.TotalCents) {
		log.Warnw("paid quote does not match the current draft, publishing anyway",
			"processId", vp.ID.Hex(), "paidCents", payment.AmountCents, "quotedCents", quote.TotalCents)
	}
	return nil, nil
}

// debitManagedProcessWallet charges a managed organization's process to its integrator's
// prepaid wallet: atomic, at most once per process (a publish retry keeps the original
// debit and passes), refused without touching the balance when it does not cover the
// price. The paid state is recorded afterwards; the wallet document itself is the
// authoritative record, so a failure there only logs.
func (a *API) debitManagedProcessWallet(vp *db.VotingProcess, org *db.Organization) error {
	quote, _, err := a.processQuote(vp)
	if err != nil {
		return err
	}
	if quote.TotalCents == 0 {
		return nil
	}
	if err := a.db.DebitWalletForProcess(org.ManagedBy, quote.TotalCents, vp.ID); err != nil {
		if err == db.ErrInsufficientWalletBalance {
			wallet, werr := a.db.Wallet(org.ManagedBy)
			if werr != nil {
				return errors.ErrInsufficientWalletBalance
			}
			return errors.ErrInsufficientWalletBalance.WithData(map[string]int64{
				"requiredCents":  quote.TotalCents,
				"availableCents": wallet.BalanceCents,
			})
		}
		return fmt.Errorf("failed to debit integrator wallet: %w", err)
	}
	if _, err := a.db.SetProcessPaymentPaidByWallet(&db.ProcessPayment{
		ProcessID:   vp.ID,
		OrgAddress:  vp.OrgAddress,
		AmountCents: quote.TotalCents,
		Currency:    "eur",
	}); err != nil {
		log.Warnw("could not record wallet-paid process payment",
			"processId", vp.ID.Hex(), "error", err)
	}
	return nil
}

// publishPaidProcess is the webhook fulfillment hook (stripe.Service.OnProcessPaid): a
// verified payment landed, publish the process as the user who requested the checkout.
// It fires only on the fulfillment that won the paid CAS, so it cannot double-publish;
// startProcessPublish's claim guards the race with a concurrent manual publish. Any
// refusal leaves the process paid — publishing later is free, never a second charge.
func (a *API) publishPaidProcess(processID bson.ObjectID) {
	vp, questions, err := a.db.ProcessWithQuestions(processID)
	if err != nil {
		log.Warnw("paid process not found for publication", "processId", processID.Hex(), "error", err)
		return
	}
	if vp.Published {
		return
	}
	payment, err := a.db.ProcessPayment(processID)
	if err != nil {
		log.Warnw("paid process has no payment record", "processId", processID.Hex(), "error", err)
		return
	}
	user, err := a.db.UserByEmail(payment.RequestedBy)
	if err != nil {
		log.Warnw("paid process cannot auto-publish: requesting user not found, publish manually",
			"processId", processID.Hex(), "requestedBy", payment.RequestedBy, "error", err)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		log.Warnw("paid process census not found", "processId", processID.Hex(), "error", err)
		return
	}
	if problems, _ := a.publishPreflightProblems(vp, questions, census, user); len(problems) > 0 {
		log.Warnw("paid process failed publish preflight, staying paid for a manual publish",
			"processId", processID.Hex(), "problems", strings.Join(problems, "; "))
		return
	}
	jobID, err := a.startProcessPublish(vp, questions, census, user)
	if err != nil {
		if err != errProcessAlreadyPublished {
			log.Warnw("could not start publication of paid process",
				"processId", processID.Hex(), "error", err)
		}
		return
	}
	log.Infow("paid process publication enqueued", "processId", processID.Hex(), "jobId", jobID)
}

// refusePaymentLocked refuses draft mutations while a payment is processing or paid: the
// amount charged (or charging) was quoted for the draft exactly as it is. Pending
// payments do not lock — the open session is expired and replaced at the next checkout.
func (a *API) refusePaymentLocked(w http.ResponseWriter, oid bson.ObjectID) bool {
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		return false // no payment; a read failure surfaces on the write path instead
	}
	if payment.Status == db.ProcessPaymentProcessing || payment.Status == db.ProcessPaymentPaid {
		errors.ErrPaymentSessionConflict.Withf("the draft is locked by its payment").Write(w)
		return true
	}
	return false
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
			// processing, paid, or an unknown stored status: fail closed rather than
			// risking a second charge
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
