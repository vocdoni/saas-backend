// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
package stripe

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// Repository defines the database operations needed by the Stripe service
type Repository interface {
	Organization(address common.Address) (*db.Organization, error)
	SetOrganization(org *db.Organization) error
	SetOrganizationSubscription(address common.Address, subscription *db.OrganizationSubscription) error
	Plan(planID uint64) (*db.Plan, error)
	PlanByStripeID(stripeID string) (*db.Plan, error)
	DefaultPlan() (*db.Plan, error)
	SetPlan(plan *db.Plan) (uint64, error)
}

// EventStore defines the interface for storing and checking webhook events for idempotency
type EventStore interface {
	EventExists(eventID string) bool
	MarkProcessed(eventID string) error
}

// Service provides the main business logic for Stripe operations
type Service struct {
	client      *Client
	repository  Repository
	eventStore  EventStore
	lockManager *LockManager
	config      *Config
}

// NewService creates a new Stripe service
func NewService(config *Config, repository Repository, eventStore EventStore) (*Service, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if repository == nil {
		return nil, fmt.Errorf("repository is required")
	}
	if eventStore == nil {
		return nil, fmt.Errorf("eventStore is required")
	}

	client := NewClient(config)
	lockManager := NewLockManager()

	return &Service{
		client:      client,
		repository:  repository,
		eventStore:  eventStore,
		lockManager: lockManager,
		config:      config,
	}, nil
}

// ProcessWebhookEvent processes a webhook event with idempotency and proper locking
func (s *Service) ProcessWebhookEvent(payload []byte, signatureHeader string) error {
	// Validate and parse the event
	event, err := s.client.ValidateWebhookEvent(payload, signatureHeader)
	if err != nil {
		return err
	}

	// Check if event was already processed (idempotency)
	if s.eventStore.EventExists(event.ID) {
		log.Debugf("stripe webhook: event %s already processed, skipping", event.ID)
		return nil
	}

	// Process the event based on its type
	var processingErr error
	switch event.Type {
	case stripeapi.EventTypeCustomerSubscriptionCreated,
		stripeapi.EventTypeCustomerSubscriptionUpdated,
		stripeapi.EventTypeCustomerSubscriptionDeleted:
		processingErr = s.handleSubscription(event)
	case stripeapi.EventTypeInvoicePaymentSucceeded:
		processingErr = s.handleInvoicePayment(event)
	case stripeapi.EventTypeProductUpdated:
		processingErr = s.handleProductUpdate(event)
	default:
		log.Debugf("stripe webhook: received unhandled event type %s (event %s)", event.Type, event.ID)
		// For unknown events, we still mark them as processed to avoid reprocessing
	}

	// Mark event as processed if successful
	if processingErr == nil {
		if err := s.eventStore.MarkProcessed(event.ID); err != nil {
			log.Errorf("stripe webhook: failed to mark event %s as processed: %v", event.ID, err)
			// Don't return error here as the event was processed successfully
		}
	}

	return processingErr
}

// handleSubscription processes a subscription creation or update event
func (s *Service) handleSubscription(event *stripeapi.Event) error {
	subscriptionInfo, err := s.client.ParseSubscriptionFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse subscription from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(subscriptionInfo.OrgAddress)
	defer unlock()

	// Get organization
	org, err := s.repository.Organization(subscriptionInfo.OrgAddress)
	if err != nil || org == nil {
		return NewStripeError("organization_not_found",
			fmt.Sprintf("organization %s not found for subscription %s",
				subscriptionInfo.OrgAddress, subscriptionInfo.ID), err)
	}

	// Handle different subscription statuses
	switch subscriptionInfo.Status {
	case stripeapi.SubscriptionStatusActive:
		return s.handleSubscriptionCreateOrUpdate(subscriptionInfo, org)
	case stripeapi.SubscriptionStatusCanceled,
		stripeapi.SubscriptionStatusUnpaid:
		return s.handleSubscriptionCancellation(subscriptionInfo.ID, org)
	default:
		// No action needed for other statuses
	}
	return nil
}

// handleSubscriptionCreateOrUpdate handles creating (or updating) a subscription.
func (s *Service) handleSubscriptionCreateOrUpdate(subscriptionInfo *SubscriptionInfo, org *db.Organization) error {
	// Get plan by Stripe product ID
	plan, err := s.repository.PlanByStripeID(subscriptionInfo.ProductID)
	if err != nil || plan == nil {
		return NewStripeError("plan_not_found",
			fmt.Sprintf("plan with Stripe ID %s not found for subscription %s",
				subscriptionInfo.ProductID, subscriptionInfo.ID), err)
	}

	org.Subscription.PlanID = plan.ID
	org.Subscription.StripeSubscriptionID = subscriptionInfo.ID
	org.Subscription.BillingPeriod = db.BillingPeriod(subscriptionInfo.BillingPeriod)
	org.Subscription.StartDate = subscriptionInfo.StartDate
	org.Subscription.RenewalDate = subscriptionInfo.EndDate
	org.Subscription.Active = (subscriptionInfo.Status == stripeapi.SubscriptionStatusActive)
	org.Subscription.Email = subscriptionInfo.Customer.Email

	// Save subscription
	if err := s.repository.SetOrganization(org); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to save subscription %s (planID=%d, status=%s) for organization %s",
				subscriptionInfo.ID, plan.ID, subscriptionInfo.Status, subscriptionInfo.OrgAddress), err)
	}

	// Update if needed customer metadata with organization address
	if len(subscriptionInfo.Customer.Metadata["address"]) > 0 {
		return NewStripeError("metadata_mismatch", "customer metadata address mismatch", nil)
	}
	if err := s.client.UpdateCustomerMetadata(
		subscriptionInfo.Customer.ID,
		map[string]string{"address": subscriptionInfo.OrgAddress.String()},
	); err != nil {
		log.Warnf("stripe webhook: failed to update customer %s metadata: %v",
			subscriptionInfo.Customer.ID, err)
	}
	log.Infof("stripe webhook: subscription %s (planID=%d, status=%s) saved for organization %s",
		subscriptionInfo.ID, plan.ID, subscriptionInfo.Status, subscriptionInfo.OrgAddress)
	return nil
}

// handleSubscriptionCancellation handles a canceled subscription by switching to the default plan
func (s *Service) handleSubscriptionCancellation(subscriptionID string, org *db.Organization) error {
	// Get default plan
	defaultPlan, err := s.repository.DefaultPlan()
	if err != nil || defaultPlan == nil {
		return NewStripeError("plan_not_found", "default plan not found", err)
	}

	// Create subscription with default plan
	orgSubscription := &db.OrganizationSubscription{
		PlanID:               defaultPlan.ID,
		StripeSubscriptionID: "",
		StartDate:            time.Now(),
		LastPaymentDate:      org.Subscription.LastPaymentDate,
		Active:               true,
	}

	if err := s.repository.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to cancel subscription %s for organization %s",
				subscriptionID, org.Address), err)
	}

	log.Infof("stripe webhook: subscription %s canceled for organization %s, switched to default plan",
		subscriptionID, org.Address)
	return nil
}

// handleInvoicePayment processes a successful payment event
func (s *Service) handleInvoicePayment(event *stripeapi.Event) error {
	invoiceInfo, err := s.client.ParseInvoiceFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse invoice from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(invoiceInfo.OrgAddress)
	defer unlock()

	// Get organization
	org, err := s.repository.Organization(invoiceInfo.OrgAddress)
	if err != nil || org == nil {
		return NewStripeError("organization_not_found",
			fmt.Sprintf("organization %s not found for payment %s",
				invoiceInfo.OrgAddress, invoiceInfo.ID), err)
	}

	// Update last payment date
	org.Subscription.LastPaymentDate = invoiceInfo.PaymentTime
	if err := s.repository.SetOrganization(org); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to update payment date for organization %s",
				invoiceInfo.OrgAddress), err)
	}

	log.Infof("stripe webhook: payment %s processed for organization %s",
		invoiceInfo.ID, invoiceInfo.OrgAddress)
	return nil
}

// handleProductUpdate processes a product update event
func (s *Service) handleProductUpdate(event *stripeapi.Event) error {
	product, err := s.client.ParseProductFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse product from event: %w", err)
	}

	// Get the existing plan by Stripe product ID
	existingPlan, err := s.repository.PlanByStripeID(product.ID)
	if err != nil || existingPlan == nil {
		// If plan doesn't exist in our database, we can skip this update
		// This might happen if the product is not one of our configured plans
		log.Debugf("stripe webhook: product %s not found in database, skipping update", product.ID)
		return nil
	}

	prices, err := s.client.GetProductPrices(product.ID)
	if err != nil || len(prices) < 2 {
		return fmt.Errorf("failed to get prices for product %s: %w", product.ID, err)
	}

	// Update the plan with new product information
	updatedPlan, err := processProductToPlan(existingPlan.ID, product, prices)
	if err != nil {
		return fmt.Errorf("failed to process updated product %s: %w", product.ID, err)
	}

	// Update the plan in the database
	if _, err := s.repository.SetPlan(updatedPlan); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to update plan for product %s", product.ID), err)
	}

	log.Infof("stripe webhook: product %s updated, plan %d refreshed", product.ID, updatedPlan.ID)
	return nil
}

// CreateCheckoutSessionWithLookupKey creates a new checkout session by resolving the lookup key to get the plan
func (s *Service) CreateCheckoutSessionWithLookupKey(
	lookupKey uint64, billingPeriod, returnURL string, orgAddress common.Address, locale string,
) (*stripeapi.CheckoutSession, error) {
	// Resolve the lookup key to get the plan
	plan, err := s.repository.Plan(lookupKey)
	if err != nil {
		return nil, NewStripeError("plan_not_found", fmt.Sprintf("plan with lookup key %d not found", lookupKey), err)
	}

	org, err := s.repository.Organization(orgAddress)
	if err != nil {
		return nil, NewStripeError("org_not_found", fmt.Sprintf("organization %s not found", orgAddress), err)
	}

	// Create checkout session parameters with the resolved Stripe price ID
	params := &CheckoutSessionParams{
		ReturnURL:     returnURL,
		OrgAddress:    orgAddress.Hex(),
		CustomerEmail: org.Creator,
		Locale:        locale,
		Quantity:      1,
	}

	if billingPeriod == string(db.BillingPeriodMonthly) && plan.StripeMonthlyPriceID != "" {
		params.PriceID = plan.StripeMonthlyPriceID
	} else if billingPeriod == string(db.BillingPeriodAnnual) && plan.StripeYearlyPriceID != "" {
		params.PriceID = plan.StripeYearlyPriceID
	} else {
		return nil, NewStripeError("invalid_billing_period",
			fmt.Sprintf("invalid billing period %s for plan %d", billingPeriod, plan.ID), nil)
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
		ID:           planID,
		Name:         product.Name,
		StripeID:     product.ID,
		Default:      isDefaultPlan(product),
		Organization: organizationData,
		VotingTypes:  votingTypesData,
		Features:     featuresData,
	}

	for _, price := range prices {
		switch price.Recurring.Interval {
		case stripeapi.PriceRecurringIntervalYear:
			plan.StripeYearlyPriceID = price.ID
			plan.YearlyPrice = price.UnitAmount
		case stripeapi.PriceRecurringIntervalMonth:
			plan.StripeMonthlyPriceID = price.ID
			plan.MonthlyPrice = price.UnitAmount
		default:
			// Ignore non-recurring prices
		}
	}
	if len(plan.StripeMonthlyPriceID) == 0 || len(plan.StripeYearlyPriceID) == 0 {
		return nil, fmt.Errorf("both monthly and yearly prices are required for plan %s", product.ID)
	}

	return plan, nil
}

// extractPlanMetadata extracts and parses plan metadata from a JSON string.
// It unmarshals the JSON data into the specified type T and returns it.
func extractPlanMetadata[T any](metadataValue string) (T, error) {
	var result T
	if err := json.Unmarshal([]byte(metadataValue), &result); err != nil {
		return result, fmt.Errorf("error parsing plan metadata JSON: %s", err.Error())
	}
	return result, nil
}

func isDefaultPlan(product *stripeapi.Product) bool {
	return product.DefaultPrice.Metadata["Default"] == "true"
}
