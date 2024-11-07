package api

import (
	"io"
	"net/http"
	"time"

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

		log.Warnw("Stripe Webhook: Error reading request body: %v\n", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	signatureHeader := r.Header.Get("Stripe-Signature")
	event, err := a.stripe.DecodeEvent(payload, signatureHeader)
	if err != nil {
		log.Warnw("Stripe Webhook: Error decoding event: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Unmarshal the event data into an appropriate struct depending on its Type
	switch event.Type {
	case "customer.subscription.created":
		customer, subscription, err := a.stripe.GetInfoFromEvent(*event)
		if err != nil {
			log.Warnw("Stripe Webhook: Error getting info from event: %v\n", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		org, _, err := a.db.OrganizationByCreatorEmail(customer.Email, false)
		if err != nil || org == nil {
			log.Warnf("Could not update subscription %s, a corresponding organization was not found.", subscription.ID)

			log.Warnf("Please do manually for creator %s \n Error:  %s", customer.Email, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		dbSubscription, err := a.db.PlanByStripeId(subscription.Items.Data[0].Plan.Product.ID)
		if err != nil || dbSubscription == nil {
			log.Warnf("Could not update subscription %s, a corresponding subscription was not found.",
				subscription.ID)
			log.Warnf("Please do manually: %s", err.Error())
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
		}

		// TODO will only worked for new subscriptions
		if err := a.db.AddSubscriptionToOrganization(org.Address, organizationSubscription); err != nil {
			log.Warnf("Could not update subscription %s for organization %s: %s", subscription.ID, org.Address, err.Error())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}
