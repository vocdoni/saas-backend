package stripe

import (
	"encoding/json"
	"fmt"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/customer"
	"github.com/stripe/stripe-go/v81/price"
	"github.com/stripe/stripe-go/v81/product"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

var ProductsIDs = []string{
	"prod_R3LTVsjklmuQAL", // Essential
	"prod_R0kTryoMNl8I19", // Premium
	"prod_RFObcbvED7MYbz", // Free
	"prod_RHurAb3OjkgJRy", // Custom
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
	params := &stripe.PriceSearchParams{
		SearchParams: stripe.SearchParams{
			Query: fmt.Sprintf("active:'true' AND lookup_key:'%s'", priceID),
		},
	}
	params.AddExpand("data.tiers")
	if results := price.Search(params); results.Next() {
		return results.Price()
	}
	return nil
}

func (s *StripeClient) GetProductByID(productID string) (*stripe.Product, error) {
	params := &stripe.ProductParams{}
	params.AddExpand("default_price")
	params.AddExpand("default_price.tiers")
	product, err := product.Get(productID, params)
	if err != nil {
		return nil, err
	}
	return product, nil
}

func (s *StripeClient) GetPrices(priceIDs []string) []*stripe.Price {
	var prices []*stripe.Price
	for _, priceID := range priceIDs {
		if price := s.GetPriceByID(priceID); price != nil {
			prices = append(prices, price)
		}
	}
	return prices
}

func (s *StripeClient) GetPlans() ([]*db.Plan, error) {
	var plans []*db.Plan
	for i, productID := range ProductsIDs {
		if product, err := s.GetProductByID(productID); product != nil && err == nil {
			var organizationData db.PlanLimits
			if err := json.Unmarshal([]byte(product.Metadata["organization"]), &organizationData); err != nil {
				return nil, fmt.Errorf("error parsing plan organization metadata JSON: %s\n", err.Error())
			}
			var votingTypesData db.VotingTypes
			if err := json.Unmarshal([]byte(product.Metadata["votingTypes"]), &votingTypesData); err != nil {
				return nil, fmt.Errorf("error parsing plan voting types metadata JSON: %s\n", err.Error())
			}
			var featuresData db.Features
			if err := json.Unmarshal([]byte(product.Metadata["features"]), &featuresData); err != nil {
				return nil, fmt.Errorf("error parsing plan features metadata JSON: %s\n", err.Error())
			}
			price := product.DefaultPrice
			startingPrice := price.UnitAmount
			if len(price.Tiers) > 0 {
				startingPrice = price.Tiers[0].FlatAmount
			}
			var tiers []db.PlanTier
			for _, tier := range price.Tiers {
				if tier.UpTo == 0 {
					continue
				}
				tiers = append(tiers, db.PlanTier{
					Amount: tier.FlatAmount,
					UpTo:   tier.UpTo,
				})
			}
			plans = append(plans, &db.Plan{
				ID:              uint64(i),
				Name:            product.Name,
				StartingPrice:   startingPrice,
				StripeID:        price.ID,
				Default:         price.Metadata["Default"] == "true",
				Organization:    organizationData,
				VotingTypes:     votingTypesData,
				Features:        featuresData,
				CensusSizeTiers: tiers,
			})
		} else {
			return nil, fmt.Errorf("error getting product %s: %s", productID, err.Error())
		}
	}
	return plans, nil
}
