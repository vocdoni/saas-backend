package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// handleWebhook handles the incoming webhook event from Stripe.
// It takes the API data and signature as input parameters and returns the session ID and an error (if any).
// The request body and Stripe-Signature header are passed to ConstructEvent, along with the webhook signing key.
// If the event type is "customer.subscription.created", it unmarshals the event data into a CheckoutSession struct
// and returns the session ID. Otherwise, it returns an empty string.
func (a *API) handleWebhook(w http.ResponseWriter, r *http.Request) {
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
		customer, subscription, err := a.stripe.GetInfoFromEvent(*event)
		if err != nil {
			log.Errorf("stripe webhook: error getting info from event: %s\n", err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		address := subscription.Metadata["address"]
		if len(address) == 0 {
			log.Errorf("subscription %s does not contain an address in metadata", subscription.ID)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		org, _, err := a.db.Organization(address, false)
		if err != nil || org == nil {
			log.Errorf("could not update subscription %s, a corresponding organization with address %s was not found.",
				subscription.ID, address)
			log.Errorf("please do manually for creator %s \n  Error:  %s", customer.Email, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		dbSubscription, err := a.db.PlanByStripeId(subscription.Items.Data[0].Plan.Product.ID)
		if err != nil || dbSubscription == nil {
			log.Errorf("could not update subscription %s, a corresponding subscription was not found.",
				subscription.ID)
			log.Errorf("please do manually: %s", err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		startDate := time.Unix(subscription.CurrentPeriodStart, 0)
		endDate := time.Unix(subscription.CurrentPeriodEnd, 0)
		renewalDate := time.Unix(subscription.BillingCycleAnchor, 0)

		organizationSubscription := &db.OrganizationSubscription{
			PlanID:        dbSubscription.ID,
			StartDate:     startDate,
			EndDate:       endDate,
			RenewalDate:   renewalDate,
			Active:        subscription.Status == "active",
			MaxCensusSize: int(subscription.Items.Data[0].Quantity),
			Email:         customer.Email,
		}

		// TODO will only worked for new subscriptions
		if err := a.db.SetOrganizationSubscription(org.Address, organizationSubscription); err != nil {
			log.Errorf("could not update subscription %s for organization %s: %s", subscription.ID, org.Address, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		log.Debugf("stripe webhook: subscription %s for organization %s processed successfully", subscription.ID, org.Address)
	case "customer.subscription.updated", "customer.subscription.deleted":
		customer, subscription, err := a.stripe.GetInfoFromEvent(*event)
		if err != nil {
			log.Errorf("stripe webhook: error getting info from event: %s\n", err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		address := subscription.Metadata["address"]
		if len(address) == 0 {
			log.Errorf("subscription %s does not contain an address in metadata", subscription.ID)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		org, _, err := a.db.Organization(address, false)
		if err != nil || org == nil {
			log.Errorf("could not update subscription %s, a corresponding organization with address %s was not found.",
				subscription.ID, address)
			log.Errorf("please do manually for creator %s \n  Error:  %s", customer.Email, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if subscription.Status == "canceled" && org.Subscription.Active {
			// replace organization subscription with the default plan
			defaultPlan, err := a.db.DefaultPlan()
			if err != nil || defaultPlan == nil {
				ErrNoDefaultPLan.WithErr((err)).Write(w)
				return
			}
			orgSubscription := &db.OrganizationSubscription{
				PlanID:        defaultPlan.ID,
				StartDate:     time.Now(),
				Active:        true,
				MaxCensusSize: defaultPlan.Organization.MaxCensus,
			}
			if err := a.db.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
				log.Errorf("could not cancel subscription %s for organization %s: %s", subscription.ID, org.Address, err.Error())
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else if subscription.Status == "active" && !org.Subscription.Active {
			org.Subscription.Active = true
			if err := a.db.SetOrganization(org); err != nil {
				log.Errorf("could activate organizations  %s subscription to active: %s", org.Address, err.Error())
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		log.Debugf("stripe webhook: subscription %s for organization %s processed as %s successfully",
			subscription.ID, org.Address, subscription.Status)
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) createSubscriptionCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	checkout := &SubscriptionCheckout{}
	if err := json.NewDecoder(r.Body).Decode(checkout); err != nil {
		ErrMalformedBody.Write(w)
		return
	}

	if checkout.LookupKey == "" || checkout.ReturnURL == "" ||
		checkout.Amount == "" || checkout.Address == "" {
		ErrMalformedBody.Withf("Missing required fields").Write(w)
		return
	}

	lookupKey, err := strconv.ParseUint(checkout.LookupKey, 10, 64)
	if err != nil {
		ErrMalformedURLParam.Withf("Invalid plan lookup key: %v", err).Write(w)
		return
	}

	amount, err := strconv.ParseInt(checkout.Amount, 10, 64)
	if err != nil {
		ErrMalformedURLParam.Withf("Invalid census amount: %v", err).Write(w)
		return
	}

	plan, err := a.db.Plan(lookupKey)
	if err != nil {
		ErrMalformedURLParam.Withf("Plan not found: %v", err).Write(w)
		return
	}

	session, err := a.stripe.CreateSubscriptionCheckoutSession(plan.StripePriceID, checkout.ReturnURL, checkout.Address, amount)
	if err != nil {
		ErrStripeError.Withf("Cannot create session: %v", err).Write(w)
		return
	}

	data := &struct {
		ClientSecret string `json:"clientSecret"`
	}{
		ClientSecret: session.ClientSecret,
	}
	httpWriteJSON(w, data)
}

func (a *API) checkoutSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		ErrMalformedURLParam.Withf("sessionID is required").Write(w)
		return
	}
	status, err := a.stripe.RetrieveCheckoutSession(sessionID)
	if err != nil {
		ErrStripeError.Withf("Cannot get session: %v", err).Write(w)
		return
	}

	httpWriteJSON(w, status)
}
