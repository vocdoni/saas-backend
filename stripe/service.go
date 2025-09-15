// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
package stripe

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v81"
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

// HandleWebhookEvent processes a webhook event with idempotency
func (s *Service) HandleWebhookEvent(payload []byte, signatureHeader string) error {
	// Validate and parse the event
	event, err := s.client.ValidateWebhookEvent(payload, signatureHeader)
	if err != nil {
		return err
	}

	// Check if event was already processed (idempotency)
	if _, alreadyProcessed := s.processedEvents.Load(event.ID); alreadyProcessed {
		log.Debugf("stripe webhook: event %s already processed, skipping", event.ID)
		return nil
	}

	// Process the event based on its type
	if err := s.HandleEvent(event); err != nil {
		return err
	}

	// Mark event as processed if successful
	s.processedEvents.Store(event.ID, time.Now())

	return nil
}

// CreateCheckoutSessionWithLookupKey creates a new checkout session by resolving the lookup key to get the plan
func (s *Service) CreateCheckoutSessionWithLookupKey(
	lookupKey uint64, returnURL string, orgAddress common.Address, locale string,
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

	// Create checkout session parameters with the resolved Stripe price ID
	params := &CheckoutSessionParams{
		PriceID:       plan.StripePriceID,
		ReturnURL:     returnURL,
		OrgAddress:    orgAddress.Hex(),
		CustomerEmail: org.Creator,
		Locale:        locale,
		Quantity:      1,
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

		plan, err := processProductToPlan(uint64(i), product)
		if err != nil {
			return nil, fmt.Errorf("failed to process product %s: %w", p.ProductID, err)
		}

		plans = append(plans, plan)
	}

	return plans, nil
}

// processProductToPlan converts a Stripe product to a database plan
func processProductToPlan(planID uint64, product *stripeapi.Product) (*db.Plan, error) {
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

	return &db.Plan{
		ID:            planID,
		Name:          product.Name,
		StartingPrice: product.DefaultPrice.UnitAmount,
		StripeID:      product.ID,
		StripePriceID: product.DefaultPrice.ID,
		Default:       isDefaultPlan(product),
		Organization:  organizationData,
		VotingTypes:   votingTypesData,
		Features:      featuresData,
	}, nil
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
