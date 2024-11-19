package stripe

import (
	"encoding/json"
	"fmt"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/customer"
	"github.com/stripe/stripe-go/v81/price"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

var PricesLookupKeys = []string{
	"essential_annual_plan",
	"premium_annual_plan",
	"free_plan",
}

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
		log.Errorf("stripe webhook: error while parsing basic request. %s\n", err.Error())
		return nil, err
	}

	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		log.Errorf("stripe webhook: webhook signature verification failed. %s\n", err.Error())
		return nil, err
	}
	return &event, nil
}

// GetInfoFromEvent processes a Stripe event to extract customer and subscription information.
func (s *StripeClient) GetInfoFromEvent(event stripe.Event) (*stripe.Customer, *stripe.Subscription, error) {
	var subscription stripe.Subscription
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		log.Errorf("error parsing webhook JSON: %s\n", err.Error())
		return nil, nil, err
	}

	params := &stripe.CustomerParams{}
	customer, err := customer.Get(subscription.Customer.ID, params)
	if err != nil || customer == nil {
		log.Errorf("could not update subscription %s, stripe internal error getting customer", subscription.ID)
		return nil, nil, err
	}
	return customer, &subscription, nil
}

func (s *StripeClient) GetPriceByID(priceID string) *stripe.Price {
	query := fmt.Sprintf("active:'true' AND lookup_key:'%s'", priceID)
	params := &stripe.PriceSearchParams{
		SearchParams: stripe.SearchParams{
			Query: query,
		},
	}
	params.AddExpand("data.tiers")
	results := price.Search(params)
	if results.Next() {
		return results.Price()
	}
	return nil
}

func (s *StripeClient) GetPrices(priceIDs []string) []*stripe.Price {
	var prices []*stripe.Price
	for _, priceID := range priceIDs {
		price := s.GetPriceByID(priceID)
		if price != nil {
			prices = append(prices, price)
		}
	}
	return prices
}

func (s *StripeClient) GetPlans() ([]*db.Plan, error) {
	var plans []*db.Plan
	for i, priceID := range PricesLookupKeys {
		price := s.GetPriceByID(priceID)
		if price != nil {
			var organizationData map[string]int
			if err := json.Unmarshal([]byte(price.Metadata["Organization"]), &organizationData); err != nil {
				return nil, fmt.Errorf("error parsing plan organization metadata JSON: %s\n", err.Error())
			}
			var votingTypesData map[string]bool
			if err := json.Unmarshal([]byte(price.Metadata["VotingTypes"]), &votingTypesData); err != nil {
				return nil, fmt.Errorf("error parsing plan voting types metadata JSON: %s\n", err.Error())
			}
			var featuresData map[string]bool
			if err := json.Unmarshal([]byte(price.Metadata["Features"]), &featuresData); err != nil {
				return nil, fmt.Errorf("error parsing plan features metadata JSON: %s\n", err.Error())
			}
			startingPrice := price.UnitAmount
			if len(price.Tiers) > 0 {
				startingPrice = price.Tiers[0].FlatAmount
			}
			plans = append(plans, &db.Plan{
				ID:            uint64(i),
				Name:          price.Nickname,
				StartingPrice: startingPrice,
				StripeID:      price.ID,
				Default:       price.Metadata["Default"] == "true",
				Organization: db.PlanLimits{
					Memberships: organizationData["Memberships"],
					SubOrgs:     organizationData["SubOrgs"],
					CensusSize:  organizationData["CensusSize"],
				},
				VotingTypes: db.VotingTypes{
					Approval: votingTypesData["Approval"],
					Ranked:   votingTypesData["Ranked"],
					Weighted: votingTypesData["Weighted"],
				},
				Features: db.Features{
					Personalization: featuresData["Personalization"],
					EmailReminder:   featuresData["EmailReminder"],
					SmsNotification: featuresData["SmsNotification"],
				},
			})
		}
	}
	return plans, nil
}
