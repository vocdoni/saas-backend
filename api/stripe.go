package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v81"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	stripeService "github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

var mu sync.Mutex

// handleWebhook godoc
//	@Summary		Handle Stripe webhook events
//	@Description	Process incoming webhook events from Stripe for subscription management. Handles subscription creation,
//	@Description	updates, deletions, and payment events.
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Param			body	body		string	true	"Stripe webhook payload"
//	@Success		200		{string}	string	"OK"
//	@Failure		400		{string}	string	"Bad Request"
//	@Failure		500		{string}	string	"Internal Server Error"
//	@Router			/subscriptions/webhook [post]
func (a *API) handleWebhook(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	const MaxBodyBytes = int64(65536)
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		log.Errorf("stripe webhook: Error reading request body: %s\n", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	signatureHeader := r.Header.Get("Stripe-Signature")
	event, err := a.stripe.DecodeEvent(payload, signatureHeader)
	if err != nil {
		log.Errorf("stripe webhook: error decoding event: %s\n", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Unmarshal the event data into an appropriate struct depending on its Type
	switch event.Type {
	case "customer.subscription.created":
		log.Infof("received stripe event Type: %s", event.Type)
		stripeSubscriptionInfo, org, err := a.getSubscriptionOrgInfo(event)
		if err != nil {
			log.Errorf("could not update subscription %s, a corresponding organization with address %s was not found.",
				stripeSubscriptionInfo.ID, stripeSubscriptionInfo.OrganizationAddress)
			log.Errorf("please do manually for creator %s \n  Error:  %s", stripeSubscriptionInfo.CustomerEmail, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		dbSubscription, err := a.db.PlanByStripeId(stripeSubscriptionInfo.ProductID)
		if err != nil || dbSubscription == nil {
			log.Errorf("could not update subscription %s, a corresponding subscription was not found.",
				stripeSubscriptionInfo.ID)
			log.Errorf("please do manually: %s", err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		organizationSubscription := &db.OrganizationSubscription{
			PlanID:        dbSubscription.ID,
			StartDate:     stripeSubscriptionInfo.StartDate,
			RenewalDate:   stripeSubscriptionInfo.EndDate,
			Active:        stripeSubscriptionInfo.Status == "active",
			MaxCensusSize: stripeSubscriptionInfo.Quantity,
			Email:         stripeSubscriptionInfo.CustomerEmail,
		}

		// TODO will only worked for new subscriptions
		if err := a.db.SetOrganizationSubscription(org.Address, organizationSubscription); err != nil {
			log.Errorf("could not update subscription %s for organization %s: %s", stripeSubscriptionInfo.ID, org.Address, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.Infof("stripe webhook: subscription %s for organization %s processed successfully", stripeSubscriptionInfo.ID, org.Address)

	case "customer.subscription.updated", "customer.subscription.deleted":
		log.Infof("received stripe event Type: %s", event.Type)
		stripeSubscriptionInfo, org, err := a.getSubscriptionOrgInfo(event)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		orgPlan, err := a.db.Plan(org.Subscription.PlanID)
		if err != nil || orgPlan == nil {
			log.Errorf("could not update subscription %s", stripeSubscriptionInfo.ID)
			log.Errorf("a corresponding plan with id %d for organization with address %s was not found",
				org.Subscription.PlanID, stripeSubscriptionInfo.OrganizationAddress)
			log.Errorf("please do manually for creator %s \n  Error:  %s", stripeSubscriptionInfo.CustomerEmail, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if stripeSubscriptionInfo.Status == "canceled" && stripeSubscriptionInfo.ProductID == orgPlan.StripeID {
			// replace organization subscription with the default plan
			defaultPlan, err := a.db.DefaultPlan()
			if err != nil || defaultPlan == nil {
				errors.ErrNoDefaultPlan.WithErr((err)).Write(w)
				return
			}
			orgSubscription := &db.OrganizationSubscription{
				PlanID:          defaultPlan.ID,
				StartDate:       time.Now(),
				LastPaymentDate: org.Subscription.LastPaymentDate,
				Active:          true,
				MaxCensusSize:   defaultPlan.Organization.MaxCensus,
			}
			if err := a.db.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
				log.Errorf("could not cancel subscription %s for organization %s: %s", stripeSubscriptionInfo.ID, org.Address, err.Error())
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else if stripeSubscriptionInfo.Status == "active" && !org.Subscription.Active {
			org.Subscription.Active = true
			if err := a.db.SetOrganization(org); err != nil {
				log.Errorf("could not activate organizations  %s subscription to active: %s", org.Address, err.Error())
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		log.Infof("stripe webhook: subscription %s for organization %s processed as %s successfully",
			stripeSubscriptionInfo.ID, org.Address, stripeSubscriptionInfo.Status)
	case "invoice.payment_succeeded":
		paymentTime, orgAddress, err := a.stripe.GetInvoiceInfoFromEvent(*event)
		if err != nil {
			log.Errorf("could not update payment from event: %s \tEvent Type:%s \tError: %v", event.ID, event.Type, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		org, _, err := a.db.Organization(orgAddress, false)
		if err != nil || org == nil {
			log.Errorf("could not update payment from event because could not retrieve organization: %s \tEvent Type:%s",
				event.ID, event.Type)
		}
		org.Subscription.LastPaymentDate = paymentTime
		if err := a.db.SetOrganization(org); err != nil {
			log.Errorf("could not update payment from event: %s \tEvent Type:%s \tError: %v", event.ID, event.Type, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		log.Infof("stripe webhook: payment %s for organization %s processed successfully", event.ID, org.Address)
	default:
		log.Infof("received stripe event: %s with type: %s", event.ID, event.Type)
	}
	w.WriteHeader(http.StatusOK)
}

// createSubscriptionCheckoutHandler godoc
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
func (a *API) createSubscriptionCheckoutHandler(w http.ResponseWriter, r *http.Request) {
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

	if checkout.Amount == 0 || checkout.Address == "" {
		errors.ErrMalformedBody.Withf("Missing required fields").Write(w)
		return
	}

	if !user.HasRoleFor(checkout.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	org, _, err := a.db.Organization(checkout.Address, false)
	if err != nil {
		errors.ErrOrganizationNotFound.Withf("Error retrieving organization: %v", err).Write(w)
		return
	}
	if org == nil {
		errors.ErrOrganizationNotFound.Withf("Organization not found: %v", err).Write(w)
		return
	}

	plan, err := a.db.Plan(checkout.LookupKey)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("Plan not found: %v", err).Write(w)
		return
	}

	session, err := a.stripe.CreateSubscriptionCheckoutSession(
		plan.StripePriceID, checkout.ReturnURL, checkout.Address, org.Creator, checkout.Locale, checkout.Amount)
	if err != nil {
		errors.ErrStripeError.Withf("Cannot create session: %v", err).Write(w)
		return
	}

	data := &struct {
		ClientSecret string `json:"clientSecret"`
		SessionID    string `json:"sessionID"`
	}{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
	}
	apicommon.HttpWriteJSON(w, data)
}

// checkoutSessionHandler godoc
//	@Summary		Get checkout session status
//	@Description	Retrieve the status of a Stripe checkout session
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Param			sessionID	path		string	true	"Checkout session ID"
//	@Success		200			{object}	stripe.CheckoutSession
//	@Failure		400			{object}	errors.Error	"Invalid session ID"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/subscriptions/checkout/{sessionID} [get]
func (a *API) checkoutSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		errors.ErrMalformedURLParam.Withf("sessionID is required").Write(w)
		return
	}
	status, err := a.stripe.RetrieveCheckoutSession(sessionID)
	if err != nil {
		errors.ErrStripeError.Withf("Cannot get session: %v", err).Write(w)
		return
	}

	apicommon.HttpWriteJSON(w, status)
}

// getSubscriptionOrgInfo is a helper function that retrieves the subscription information from
// the subscription event and the Organization information from the database.
func (a *API) getSubscriptionOrgInfo(event *stripe.Event) (*stripeService.StripeSubscriptionInfo, *db.Organization, error) {
	stripeSubscriptionInfo, err := a.stripe.GetSubscriptionInfoFromEvent(*event)
	if err != nil {
		return nil, nil, fmt.Errorf("could not decode event for subscription: %s", err.Error())
	}
	org, _, err := a.db.Organization(stripeSubscriptionInfo.OrganizationAddress, false)
	if err != nil || org == nil {
		log.Errorf("could not update subscription %s, a corresponding organization with address %s was not found.",
			stripeSubscriptionInfo.ID, stripeSubscriptionInfo.OrganizationAddress)
		log.Errorf("please do manually for creator %s \n  Error:  %s", stripeSubscriptionInfo.CustomerEmail, err.Error())
		if org == nil {
			return nil, nil, fmt.Errorf("no organization found with address %s", stripeSubscriptionInfo.OrganizationAddress)
		} else {
			return nil, nil, fmt.Errorf("could not retrieve organization with address %s: %s",
				stripeSubscriptionInfo.OrganizationAddress, err.Error())
		}
	}

	return stripeSubscriptionInfo, org, nil
}

// createSubscriptionPortalSessionHandler godoc
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
func (a *API) createSubscriptionPortalSessionHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	session, err := a.stripe.CreatePortalSession(org.Creator)
	if err != nil {
		errors.ErrStripeError.Withf("Cannot create customer portal session: %v", err).Write(w)
		return
	}

	data := &struct {
		PortalURL string `json:"portalURL"`
	}{
		PortalURL: session.URL,
	}
	apicommon.HttpWriteJSON(w, data)
}
