package stripe

import (
	"encoding/json"

	"github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/customer"
	"github.com/stripe/stripe-go/v80/webhook"
	"go.vocdoni.io/dvote/log"
)

// StripeClient is a client for interacting with the Stripe API.
// It holds the necessary configuration such as the webhook secret.
type StripeClient struct {
	webhookSecret string
}

// New creates a new instance of StripeClient with the provided API secret and webhook secret.
// It sets the Stripe API key to the provided apiSecret.
func New(apiSecret, webhookSecret string) *StripeClient {
	stripe.Key = apiSecret
	return &StripeClient{
		webhookSecret: webhookSecret,
	}
}

// DecodeEvent decodes a Stripe webhook event from the given payload and signature header.
func (s *StripeClient) DecodeEvent(payload []byte, signatureHeader string) (*stripe.Event, error) {
	event := stripe.Event{}

	if err := json.Unmarshal(payload, &event); err != nil {
		log.Warnw("stripe webhook: error while parsing basic request. %s\n", err.Error())
		return nil, err
	}

	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		log.Warnw("stripe webhook: webhook signature verification failed. %s\n", err.Error())
		return nil, err
	}
	return &event, nil
}

// GetInfoFromEvent processes a Stripe event to extract customer and subscription information.
func (s *StripeClient) GetInfoFromEvent(event stripe.Event) (*stripe.Customer, *stripe.Subscription, error) {
	var subscription stripe.Subscription
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		log.Warnf("error parsing webhook JSON: %s\n", err.Error())
		return nil, nil, err
	}

	log.Debugf("Subscription created for %s and plan %s", subscription.ID, subscription.Customer.ID)
	params := &stripe.CustomerParams{}
	customer, err := customer.Get(subscription.Customer.ID, params)
	if err != nil || customer == nil {
		log.Warnf("could not update subscription %s, stripe internal error getting customer", subscription.ID)
		return nil, nil, err
	}
	return customer, &subscription, nil
}
