// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
package stripe

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

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

func (s *Service) HandleEvent(event *stripeapi.Event) error {
	switch event.Type {
	case stripeapi.EventTypeCustomerSubscriptionCreated,
		stripeapi.EventTypeCustomerSubscriptionUpdated,
		stripeapi.EventTypeCustomerSubscriptionDeleted:
		return s.handleSubscription(event)
	case stripeapi.EventTypeInvoicePaymentSucceeded:
		return s.handleInvoicePayment(event)
	case stripeapi.EventTypeProductUpdated:
		return s.handleProductUpdate(event)
	default:
		log.Debugf("stripe webhook: received unhandled event type %s (id %s)", event.Type, event.ID)
		return nil
	}
}

// handleSubscription processes a subscription creation or update event
func (s *Service) handleSubscription(event *stripeapi.Event) error {
	subscriptionInfo, err := parseSubscriptionFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse subscription from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(subscriptionInfo.OrgAddress)
	defer unlock()

	// Get organization
	org, err := s.db.Organization(subscriptionInfo.OrgAddress)
	if err != nil || org == nil {
		return fmt.Errorf("organization %s not found for subscription %s: %v",
			subscriptionInfo.OrgAddress, subscriptionInfo.ID, err)
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
	plan, err := s.db.PlanByStripeID(subscriptionInfo.ProductID)
	if err != nil || plan == nil {
		return fmt.Errorf("plan with Stripe ID %s not found for subscription %s: %v",
			subscriptionInfo.ProductID, subscriptionInfo.ID, err)
	}

	org.Subscription.PlanID = plan.ID
	org.Subscription.StripeSubscriptionID = subscriptionInfo.ID
	org.Subscription.BillingPeriod = db.BillingPeriod(subscriptionInfo.BillingPeriod)
	org.Subscription.StartDate = subscriptionInfo.StartDate
	org.Subscription.RenewalDate = subscriptionInfo.EndDate
	org.Subscription.Active = (subscriptionInfo.Status == stripeapi.SubscriptionStatusActive)
	org.Subscription.Email = subscriptionInfo.Customer.Email

	// Save subscription
	if err := s.db.SetOrganization(org); err != nil {
		return fmt.Errorf("failed to save subscription %s (planID=%d, status=%s) for organization %s: %v",
			subscriptionInfo.ID, plan.ID, subscriptionInfo.Status, subscriptionInfo.OrgAddress, err)
	}

	// Update if needed customer metadata with organization address
	if subscriptionInfo.Customer.Metadata["address"] != "" {
		return fmt.Errorf("customer metadata address mismatch")
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
	defaultPlan, err := s.db.DefaultPlan()
	if err != nil || defaultPlan == nil {
		return fmt.Errorf("default plan not found: %v", err)
	}

	// Create subscription with default plan
	orgSubscription := &db.OrganizationSubscription{
		PlanID:               defaultPlan.ID,
		StripeSubscriptionID: "",
		StartDate:            time.Now(),
		LastPaymentDate:      org.Subscription.LastPaymentDate,
		Active:               true,
	}

	if err := s.db.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
		return fmt.Errorf("failed to cancel subscription %s for organization %s: %v",
			subscriptionID, org.Address, err)
	}

	log.Infof("stripe webhook: subscription %s canceled for organization %s, switched to default plan",
		subscriptionID, org.Address)
	return nil
}

// handleInvoicePayment processes a successful payment event
func (s *Service) handleInvoicePayment(event *stripeapi.Event) error {
	invoiceInfo, err := parseInvoiceFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse invoice from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(invoiceInfo.OrgAddress)
	defer unlock()

	// Get organization
	org, err := s.db.Organization(invoiceInfo.OrgAddress)
	if err != nil || org == nil {
		return fmt.Errorf("organization %s not found for payment %s: %v",
			invoiceInfo.OrgAddress, invoiceInfo.ID, err)
	}

	// Update last payment date
	org.Subscription.LastPaymentDate = invoiceInfo.PaymentTime
	if err := s.db.SetOrganization(org); err != nil {
		return fmt.Errorf("failed to update payment date for organization %s: %v",
			invoiceInfo.OrgAddress, err)
	}

	log.Infof("stripe webhook: payment %s processed for organization %s",
		invoiceInfo.ID, invoiceInfo.OrgAddress)
	return nil
}

// handleProductUpdate processes a product update event
func (s *Service) handleProductUpdate(event *stripeapi.Event) error {
	product, err := parseProductFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse product from event: %w", err)
	}

	// Get the existing plan by Stripe product ID
	existingPlan, err := s.db.PlanByStripeID(product.ID)
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
	if _, err := s.db.SetPlan(updatedPlan); err != nil {
		return fmt.Errorf("failed to update plan for product %s: %v", product.ID, err)
	}

	log.Infof("stripe webhook: product %s updated, plan %d refreshed", product.ID, updatedPlan.ID)
	return nil
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
