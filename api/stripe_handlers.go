package api

import (
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

// Constants for webhook handling
const (
	// MaxBodyBytes caps a webhook body at all the public route group lets through (over 800 KB): an
	// event carries its whole object, a dispute's evidence alone can exceed 64 KB, and an event
	// refused here is lost for good once Stripe stops retrying it.
	MaxBodyBytes = int64(maxPublicBodyBytes) //revive:disable:unexported-naming
)

// StripeHandlers contains the Stripe service and handles HTTP requests
type StripeHandlers struct {
	service *stripe.Service
}

// NewStripeHandlers creates new Stripe HTTP handlers
func NewStripeHandlers(service *stripe.Service) *StripeHandlers {
	return &StripeHandlers{
		service: service,
	}
}

// handleWebhook godoc
//
//	@Summary		Handle Stripe webhook events
//	@Description	Process incoming webhook events from Stripe: wallet top-up and process payment checkouts,
//	@Description	refunds and disputes, with idempotency. Any other event is acknowledged and ignored.
//	@Tags			payments
//	@Accept			json
//	@Produce		json
//	@Param			body	body		string	true	"Stripe webhook payload"
//	@Success		200		{string}	string	"OK"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/payments/webhook [post]
func (h *StripeHandlers) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		errors.ErrStripeWebhookError.With("stripe service not available").Write(w)
		return
	}

	// Read and validate the request body
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		errors.ErrStripeWebhookError.With("error reading request body").WithErr(err).Write(w)
		return
	}

	// Get signature header
	signatureHeader := r.Header.Get("Stripe-Signature")
	if signatureHeader == "" {
		errors.ErrStripeWebhookError.With("missing Stripe-Signature header").Write(w)
		return
	}

	// Process the webhook event
	if err := h.service.HandleWebhookEvent(payload, signatureHeader); err != nil {
		errors.ErrStripeWebhookError.With("failed to process event").WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}

// checkoutSessionHandler godoc
//
//	@Summary		Get checkout session status
//	@Description	Retrieve the status of a Stripe checkout session. Only an admin of the organization
//	@Description	the session was created for may read it; any other session id answers 404, so the
//	@Description	existence of a session is never revealed. A top-up opened for a process (a process
//	@Description	checkout or census headroom) also returns `pricing`: the quote, the wallet balance it was
//	@Description	measured against, the shortfall and what the session charges (`censusSize` marks census
//	@Description	headroom). It explains the amount and nothing else; any other session has none.
//	@Tags			payments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			sessionId	path		string	true	"Checkout session ID"
//	@Success		200			{object}	stripe.CheckoutSessionStatus
//	@Failure		400			{object}	errors.Error	"Invalid session ID"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Checkout session not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/wallet/checkout/{sessionId} [get]
func (h *StripeHandlers) GetCheckoutSession(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}

	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	sessionID := chi.URLParam(r, "sessionId")
	if sessionID == "" {
		errors.ErrMalformedURLParam.Withf("sessionId is required").Write(w)
		return
	}

	status, err := h.service.GetCheckoutSession(sessionID)
	if errors.Is(err, errors.ErrCheckoutSessionNotFound) {
		// a missing session answers like one the caller may not read
		errors.ErrCheckoutSessionNotFound.Write(w)
		return
	}
	if err != nil {
		// any other failure (network, rate limit, outage) does not depend on the session, so a
		// generic 5xx reveals nothing about it. The Stripe error stays in the log
		log.Warnw("cannot get checkout session", "session", sessionID, "error", err)
		errors.ErrStripeError.Write(w)
		return
	}

	// only an admin of the organization the session was created for may read it; anything
	// else — no recorded organization included — is answered as if the session did not exist
	orgAddress := common.HexToAddress(status.OrgAddress)
	if status.OrgAddress == "" || orgAddress.Cmp(common.Address{}) == 0 ||
		!user.HasRoleFor(orgAddress, db.AdminRole) {
		errors.ErrCheckoutSessionNotFound.Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, status)
}

// InitializeStripeService initializes the Stripe service with proper configuration
func (a *API) InitializeStripeService() error {
	// Create Stripe configuration
	config, err := stripe.NewConfig()
	if err != nil {
		return err
	}

	// Create service
	service, err := stripe.NewService(config, a.db)
	if err != nil {
		return err
	}

	// Create handlers
	a.stripeHandlers = NewStripeHandlers(service)
	// wire the pay-per-process seam: the same service backs one-time checkout, and a
	// verified process payment triggers publication server-side
	a.paymentGW = service
	service.OnProcessTopUp = a.applyProcessTopUp
	service.OnProcessCheckoutDropped = a.dropProcessCheckout

	log.Infof("Stripe service initialized successfully")
	return nil
}
