package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
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
//	@Description	Process incoming webhook events from Stripe for subscription management. Handles subscription creation,
//	@Description	updates, deletions, and payment events with idempotency and proper error handling.
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Param			body	body		string	true	"Stripe webhook payload"
//	@Success		200		{string}	string	"OK"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/subscriptions/webhook [post]
func (h *StripeHandlers) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Read and validate the request body
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("stripe webhook: error reading request body: %s", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Get signature header
	signatureHeader := r.Header.Get("Stripe-Signature")
	if signatureHeader == "" {
		log.Errorf("stripe webhook: missing Stripe-Signature header")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Process the webhook event
	if err := h.service.ProcessWebhookEvent(r.Context(), payload, signatureHeader); err != nil {
		log.Errorf("stripe webhook: failed to process event: %v", err)

		// Check if it's a validation error (client error) or server error
		if stripeErr, ok := err.(*stripe.StripeError); ok {
			switch stripeErr.Code {
			case "webhook_validation", "invalid_event":
				w.WriteHeader(http.StatusBadRequest)
			case "organization_not_found", "plan_not_found":
				// These are business logic errors that shouldn't cause retries
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	// Success
	w.WriteHeader(http.StatusOK)
}

// createSubscriptionCheckoutHandler godoc
//
//	@Summary		Create a subscription checkout session
//	@Description	Create a new Stripe checkout session for subscription purchases
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.SubscriptionCheckout	true	"Checkout information"
//	@Success		200		{object}	map[string]string				"Contains clientSecret and sessionID"
//	@Failure		400		{object}	errors.Error					"Invalid input data"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		404		{object}	errors.Error					"Organization or plan not found"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/subscriptions/checkout [post]
func (h *StripeHandlers) CreateSubscriptionCheckout(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	checkout := &apicommon.SubscriptionCheckout{}
	if err := json.NewDecoder(r.Body).Decode(checkout); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if checkout.Amount == 0 || checkout.Address.Cmp(common.Address{}) == 0 {
		errors.ErrMalformedBody.Withf("Missing required fields").Write(w)
		return
	}

	if !user.HasRoleFor(checkout.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// Note: In the full implementation, you'd validate the plan exists and resolve lookup key to price ID
	// For now, we'll create the checkout session directly using the lookup key as string
	params := &stripe.CheckoutSessionParams{
		PriceID:             fmt.Sprintf("%d", checkout.LookupKey), // Convert uint64 to string
		ReturnURL:           checkout.ReturnURL,
		OrganizationAddress: checkout.Address.Hex(),
		CustomerEmail:       user.Email,
		Locale:              checkout.Locale,
		Quantity:            checkout.Amount,
	}

	session, err := h.service.CreateCheckoutSession(r.Context(), params)
	if err != nil {
		log.Errorf("failed to create checkout session: %v", err)
		errors.ErrStripeError.Withf("Cannot create session: %v", err).Write(w)
		return
	}

	data := &struct {
		ClientSecret string `json:"clientSecret"`
		SessionID    string `json:"sessionId"`
	}{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
	}

	apicommon.HTTPWriteJSON(w, data)
}

// checkoutSessionHandler godoc
//
//	@Summary		Get checkout session status
//	@Description	Retrieve the status of a Stripe checkout session
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Param			sessionID	path		string	true	"Checkout session ID"
//	@Success		200			{object}	stripe.CheckoutSessionStatus
//	@Failure		400			{object}	errors.Error	"Invalid session ID"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/subscriptions/checkout/{sessionID} [get]
func (h *StripeHandlers) GetCheckoutSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		errors.ErrMalformedURLParam.Withf("sessionID is required").Write(w)
		return
	}

	status, err := h.service.GetCheckoutSession(r.Context(), sessionID)
	if err != nil {
		log.Errorf("failed to get checkout session: %v", err)
		errors.ErrStripeError.Withf("Cannot get session: %v", err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, status)
}

// createSubscriptionPortalSessionHandler godoc
//
//	@Summary		Create a subscription portal session
//	@Description	Create a Stripe customer portal session for managing subscriptions
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string				true	"Organization address"
//	@Success		200		{object}	map[string]string	"Contains portalURL"
//	@Failure		400		{object}	errors.Error		"Invalid input data"
//	@Failure		401		{object}	errors.Error		"Unauthorized"
//	@Failure		404		{object}	errors.Error		"Organization not found"
//	@Failure		500		{object}	errors.Error		"Internal server error"
//	@Router			/subscriptions/{address}/portal [get]
func (h *StripeHandlers) CreateSubscriptionPortalSession(w http.ResponseWriter, r *http.Request, a *API) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// Get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}

	if !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	session, err := h.service.CreatePortalSession(r.Context(), org.Creator)
	if err != nil {
		log.Errorf("failed to create portal session: %v", err)
		errors.ErrStripeError.Withf("Cannot create customer portal session: %v", err).Write(w)
		return
	}

	data := &struct {
		PortalURL string `json:"portalURL"`
	}{
		PortalURL: session.URL,
	}

	apicommon.HTTPWriteJSON(w, data)
}

// Repository adapter to make db.MongoStorage compatible with stripe.Repository
type RepositoryAdapter struct {
	db *db.MongoStorage
}

// NewRepositoryAdapter creates a new repository adapter
func NewRepositoryAdapter(database *db.MongoStorage) *RepositoryAdapter {
	return &RepositoryAdapter{db: database}
}

// Organization implements stripe.Repository
func (r *RepositoryAdapter) Organization(address common.Address) (*db.Organization, error) {
	return r.db.Organization(address)
}

// SetOrganization implements stripe.Repository
func (r *RepositoryAdapter) SetOrganization(org *db.Organization) error {
	return r.db.SetOrganization(org)
}

// SetOrganizationSubscription implements stripe.Repository
func (r *RepositoryAdapter) SetOrganizationSubscription(address common.Address, subscription *db.OrganizationSubscription) error {
	return r.db.SetOrganizationSubscription(address, subscription)
}

// PlanByStripeID implements stripe.Repository
func (r *RepositoryAdapter) PlanByStripeID(stripeID string) (*db.Plan, error) {
	return r.db.PlanByStripeID(stripeID)
}

// DefaultPlan implements stripe.Repository
func (r *RepositoryAdapter) DefaultPlan() (*db.Plan, error) {
	return r.db.DefaultPlan()
}

// InitializeStripeService initializes the Stripe service with proper configuration
func (a *API) InitializeStripeService() error {
	// Create Stripe configuration
	config, err := stripe.NewConfig()
	if err != nil {
		return err
	}

	// Create repository adapter
	repository := NewRepositoryAdapter(a.db)

	// Create event store (in production, use Redis or database)
	eventStore := stripe.NewMemoryEventStore(24 * time.Hour)

	// Create service
	service, err := stripe.NewService(config, repository, eventStore)
	if err != nil {
		return err
	}

	// Create handlers
	a.stripeHandlers = NewStripeHandlers(service)

	log.Infof("Stripe service initialized successfully")
	return nil
}
