package stripe

import (
	"encoding/json"

	"github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/customer"
	"github.com/stripe/stripe-go/v80/webhook"
	"go.vocdoni.io/dvote/log"
)

type StripeClient struct {
	webhookSecret string
}

func New(apiSecret, webhookSecret string) *StripeClient {
	stripe.Key = apiSecret
	log.Infof("Stripe webhook key: %s", webhookSecret)
	return &StripeClient{
		webhookSecret: webhookSecret,
	}
}

func (s *StripeClient) DecodeEvent(payload []byte, signatureHeader string) (*stripe.Event, error) {
	event := stripe.Event{}

	if err := json.Unmarshal(payload, &event); err != nil {
		log.Warnw("Stripe Webhook: error while parsing basic request. %s\n", err.Error())
		return nil, err
	}

	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		log.Warnw("Stripe Webhook: Webhook signature verification failed. %s\n", err.Error())
		return nil, err
	}
	return &event, nil
}

func (s *StripeClient) GetInfoFromEvent(event stripe.Event) (*stripe.Customer, *stripe.Subscription, error) {
	var subscription stripe.Subscription
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		log.Warnf("Error parsing webhook JSON: %s\n", err.Error())
		return nil, nil, err
	}

	log.Debugf("Subscription created for %s and plan %s", subscription.ID, subscription.Customer.ID)
	params := &stripe.CustomerParams{}
	customer, err := customer.Get(subscription.Customer.ID, params)
	if err != nil || customer == nil {
		log.Warnf("Could not update subscription %s, stripe internal error getting customer", subscription.ID)
		return nil, nil, err
	}
	return customer, &subscription, nil
}
