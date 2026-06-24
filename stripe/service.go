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
	"go.vocdoni.io/dvote/log"
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
	lookupKey string, billingPeriod, returnURL string, orgAddress common.Address, locale string,
) (*stripeapi.CheckoutSession, error) {
	// Resolve the lookup key (the plan's Stripe product ID) to get the plan
	plan, err := s.db.Plan(lookupKey)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("plan with lookup key %s not found: %v", lookupKey, err)
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
		return nil, errors.ErrStripeError.Withf("invalid billing period %s for plan %s", billingPeriod, plan.ID)
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

// planProductMarkerKeys are the metadata keys every vocdoni plan product carries. A Stripe
// product is treated as one of our plans only if it defines all of them. This is the marker
// that lets the service discover plans dynamically, without a hardcoded product allowlist.
var planProductMarkerKeys = []string{"organization", "votingTypes", "features"}

// isPlanProduct reports whether a Stripe product is one of our plan products, by checking it
// carries the full plan-metadata marker.
func isPlanProduct(product *stripeapi.Product) bool {
	for _, k := range planProductMarkerKeys {
		if _, ok := product.Metadata[k]; !ok {
			return false
		}
	}
	return true
}

// GetPlansFromStripe discovers plans from Stripe and converts them to database format.
//
// Stripe is the single source of truth: every active product carrying our plan-metadata
// marker becomes a plan, so adding a plan (including a per-customer "custom" plan) is just a
// matter of creating the product in Stripe — no code or config change. A product that fails
// to parse is skipped and logged rather than aborting the whole sync, so one malformed
// product can never take down the entire catalog.
func (s *Service) GetPlansFromStripe() ([]*db.Plan, error) {
	products, err := s.client.ListProducts()
	if err != nil {
		return nil, fmt.Errorf("failed to list products: %w", err)
	}

	var plans []*db.Plan
	for i := range products {
		product := &products[i]
		if !isPlanProduct(product) {
			continue
		}
		prices, err := s.client.GetProductPrices(product.ID)
		if err != nil {
			log.Warnw("skipping plan product: failed to get prices",
				"product", product.ID, "name", product.Name, "error", err.Error())
			continue
		}
		plan, err := processProductToPlan(product, prices)
		if err != nil {
			log.Warnw("skipping plan product: failed to process",
				"product", product.ID, "name", product.Name, "error", err.Error())
			continue
		}
		plans = append(plans, plan)
	}

	return plans, nil
}

// processProductToPlan converts a Stripe product to a database plan. The plan is keyed by the
// Stripe product ID. Missing recurring prices are tolerated (a free, non-purchasable plan such
// as the free integrator tier has none); the checkout path validates price availability per
// billing period at purchase time.
func processProductToPlan(product *stripeapi.Product, prices []stripeapi.Price) (*db.Plan, error) {
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

	// integratorLimits is optional: only integrator plans carry it. Skip parsing when the
	// product has no such metadata so non-integrator plans don't fail to sync.
	var integratorLimitsData db.IntegratorLimits
	if raw := product.Metadata["integratorLimits"]; raw != "" {
		integratorLimitsData, err = extractPlanMetadata[db.IntegratorLimits](raw)
		if err != nil {
			return nil, err
		}
	}

	plan := &db.Plan{
		ID:               product.ID,
		Name:             product.Name,
		Default:          isDefaultPlan(product),
		Public:           isPublicPlan(product),
		Organization:     organizationData,
		VotingTypes:      votingTypesData,
		Features:         featuresData,
		IntegratorLimits: integratorLimitsData,
		FreeTrialDays:    0,
	}

	for _, price := range prices {
		if price.Recurring == nil {
			// Ignore one-time prices
			continue
		}
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
			// Ignore other recurring intervals
		}
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
	return product.DefaultPrice != nil && product.DefaultPrice.Metadata["Default"] == "true"
}

// isPublicPlan reports whether a plan product is listed on the public /plans catalog. Plans
// are public by default; a product opts out of the listing with metadata visibility="private"
// (used for per-customer custom plans and the internal free integrator tier).
func isPublicPlan(product *stripeapi.Product) bool {
	return product.Metadata["visibility"] != "private"
}
