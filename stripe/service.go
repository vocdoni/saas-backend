// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
package stripe

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// Service provides the main business logic for Stripe operations
type Service struct {
	client          *Client
	db              *db.MongoStorage
	processedEvents sync.Map // map[string]time.Time
	lockManager     *LockManager
	config          *Config
}

// NewService creates a new Stripe service
func NewService(config *Config, database *db.MongoStorage) (*Service, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}

	return &Service{
		client:      NewClient(config),
		db:          database,
		lockManager: NewLockManager(),
		config:      config,
	}, nil
}

// CreateCheckoutSessionWithLookupKey creates a new checkout session by resolving the lookup key to get the plan
func (s *Service) CreateCheckoutSessionWithLookupKey(
	lookupKey uint64, billingPeriod, returnURL string, orgAddress common.Address, locale string,
) (*stripeapi.CheckoutSession, error) {
	// Resolve the lookup key to get the plan
	plan, err := s.db.Plan(lookupKey)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("plan with lookup key %d not found: %v", lookupKey, err)
	}

	org, err := s.db.Organization(orgAddress)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("organization %s not found: %v", orgAddress, err)
	}

	// Determine if the organization is eligible for a free trial based on
	// if they already made a subscription and request a yearly plan
	freeTrialDays := 0
	if org.Subscription.LastPaymentDate.IsZero() && billingPeriod == string(db.BillingPeriodAnnual) {
		freeTrialDays = plan.FreeTrialDays
	}

	// Create checkout session parameters with the resolved Stripe price ID
	params := &CheckoutSessionParams{
		ReturnURL:     returnURL,
		OrgAddress:    orgAddress.Hex(),
		CustomerEmail: org.Creator,
		Locale:        locale,
		Quantity:      1,
		FreeTrialDays: freeTrialDays,
	}

	if billingPeriod == string(db.BillingPeriodMonthly) && plan.StripeMonthlyPriceID != "" {
		params.PriceID = plan.StripeMonthlyPriceID
	} else if billingPeriod == string(db.BillingPeriodAnnual) && plan.StripeYearlyPriceID != "" {
		params.PriceID = plan.StripeYearlyPriceID
	} else {
		return nil, errors.ErrStripeError.Withf("invalid billing period %s for plan %d", billingPeriod, plan.ID)
	}

	return s.client.CreateCheckoutSession(params)
}

// CreateCheckoutSession creates a new checkout session
func (s *Service) CreateCheckoutSession(params *CheckoutSessionParams) (*stripeapi.CheckoutSession, error) {
	return s.client.CreateCheckoutSession(params)
}

// GetCheckoutSession retrieves a checkout session status
func (s *Service) GetCheckoutSession(sessionID string) (*CheckoutSessionStatus, error) {
	return s.client.GetCheckoutSession(sessionID)
}

// CreatePortalSession creates a billing portal session
func (s *Service) CreatePortalSession(customerEmail string) (*stripeapi.BillingPortalSession, error) {
	return s.client.CreatePortalSession(customerEmail)
}

// GetPlansFromStripe retrieves plans from Stripe and converts them to database format
func (s *Service) GetPlansFromStripe() ([]*db.Plan, error) {
	var plans []*db.Plan

	for i, p := range s.config.Plans {
		if p.ProductID == "" {
			continue
		}

		product, err := s.client.GetProduct(p.ProductID)
		if err != nil {
			return nil, fmt.Errorf("failed to get product %s: %w", p.ProductID, err)
		}

		prices, err := s.client.GetProductPrices(p.ProductID)
		if err != nil || len(prices) < 2 {
			return nil, fmt.Errorf("failed to get prices for product %s: %w", p.ProductID, err)
		}

		plan, err := processProductToPlan(uint64(i), product, prices)
		if err != nil {
			return nil, fmt.Errorf("failed to process product %s: %w", p.ProductID, err)
		}

		plans = append(plans, plan)
	}

	return plans, nil
}

// processProductToPlan converts a Stripe product to a database plan
func processProductToPlan(planID uint64, product *stripeapi.Product, prices []stripeapi.Price) (*db.Plan, error) {
	organizationData, err := extractPlanMetadata[db.PlanLimits](product.Metadata["organization"])
	if err != nil {
		return nil, err
	}

	votingTypesData, err := extractPlanMetadata[db.VotingTypes](product.Metadata["votingTypes"])
	if err != nil {
		return nil, err
	}

	featuresData, err := extractPlanMetadata[db.Features](product.Metadata["features"])
	if err != nil {
		return nil, err
	}

	plan := &db.Plan{
		ID:            planID,
		Name:          product.Name,
		StripeID:      product.ID,
		Default:       isDefaultPlan(product),
		Organization:  organizationData,
		VotingTypes:   votingTypesData,
		Features:      featuresData,
		FreeTrialDays: 0,
	}

	for _, price := range prices {
		switch price.Recurring.Interval {
		case stripeapi.PriceRecurringIntervalYear:
			plan.StripeYearlyPriceID = price.ID
			plan.YearlyPrice = price.UnitAmount
			if price.Metadata["freeTrialDays"] != "" {
				if days, err := strconv.Atoi(price.Metadata["freeTrialDays"]); err == nil && days >= 0 {
					plan.FreeTrialDays = days
				}
			}
		case stripeapi.PriceRecurringIntervalMonth:
			plan.StripeMonthlyPriceID = price.ID
			plan.MonthlyPrice = price.UnitAmount
		default:
			// Ignore non-recurring prices
		}
	}
	if plan.StripeMonthlyPriceID == "" || plan.StripeYearlyPriceID == "" {
		return nil, fmt.Errorf("both monthly and yearly prices are required for plan %s", product.ID)
	}

	return plan, nil
}

// extractPlanMetadata extracts and parses plan metadata from a JSON string.
// It unmarshals the JSON data into the specified type T and returns it.
func extractPlanMetadata[T any](metadataValue string) (T, error) {
	var result T
	if err := json.Unmarshal([]byte(metadataValue), &result); err != nil {
		return result, fmt.Errorf("error parsing plan metadata JSON: %w", err)
	}
	return result, nil
}

func isDefaultPlan(product *stripeapi.Product) bool {
	return product.DefaultPrice.Metadata["Default"] == "true"
}
