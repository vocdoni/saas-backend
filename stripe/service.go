package stripe

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v81"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// Repository defines the database operations needed by the Stripe service
type Repository interface {
	Organization(address common.Address) (*db.Organization, error)
	SetOrganization(org *db.Organization) error
	SetOrganizationSubscription(address common.Address, subscription *db.OrganizationSubscription) error
	PlanByStripeID(stripeID string) (*db.Plan, error)
	DefaultPlan() (*db.Plan, error)
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
func (s *Service) ProcessWebhookEvent(ctx context.Context, payload []byte, signatureHeader string) error {
	// Validate and parse the event
	event, err := s.client.ValidateWebhookEvent(payload, signatureHeader)
	if err != nil {
		return err
	}

	// Check if event was already processed (idempotency)
	if s.eventStore.EventExists(event.ID) {
		log.Infof("stripe webhook: event %s already processed, skipping", event.ID)
		return nil
	}

	// Process the event based on its type
	var processingErr error
	switch event.Type {
	case "customer.subscription.created":
		processingErr = s.handleSubscriptionCreated(ctx, event)
	case "customer.subscription.updated", "customer.subscription.deleted":
		processingErr = s.handleSubscriptionUpdated(ctx, event)
	case "invoice.payment_succeeded":
		processingErr = s.handlePaymentSucceeded(ctx, event)
	default:
		log.Infof("stripe webhook: received unhandled event type %s (event %s)", event.Type, event.ID)
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

// handleSubscriptionCreated processes a subscription creation event
func (s *Service) handleSubscriptionCreated(ctx context.Context, event *stripeapi.Event) error {
	subscriptionInfo, err := s.client.ParseSubscriptionFromEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to parse subscription from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(subscriptionInfo.OrganizationAddress.Hex())
	defer unlock()

	// Get organization
	orgAddress := subscriptionInfo.OrganizationAddress
	org, err := s.repository.Organization(orgAddress)
	if err != nil || org == nil {
		return NewStripeError("organization_not_found",
			fmt.Sprintf("organization %s not found for subscription %s",
				subscriptionInfo.OrganizationAddress, subscriptionInfo.ID), err)
	}

	// Get plan by Stripe product ID
	plan, err := s.repository.PlanByStripeID(subscriptionInfo.ProductID)
	if err != nil || plan == nil {
		return NewStripeError("plan_not_found",
			fmt.Sprintf("plan with Stripe ID %s not found for subscription %s",
				subscriptionInfo.ProductID, subscriptionInfo.ID), err)
	}

	// Create organization subscription
	orgSubscription := &db.OrganizationSubscription{
		PlanID:        plan.ID,
		StartDate:     subscriptionInfo.StartDate,
		RenewalDate:   subscriptionInfo.EndDate,
		Active:        subscriptionInfo.Status == "active",
		MaxCensusSize: subscriptionInfo.Quantity,
		Email:         subscriptionInfo.CustomerEmail,
	}

	// Save subscription
	if err := s.repository.SetOrganizationSubscription(orgAddress, orgSubscription); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to save subscription %s for organization %s",
				subscriptionInfo.ID, subscriptionInfo.OrganizationAddress), err)
	}

	log.Infof("stripe webhook: subscription %s created for organization %s",
		subscriptionInfo.ID, subscriptionInfo.OrganizationAddress)
	return nil
}

// handleSubscriptionUpdated processes a subscription update or deletion event
func (s *Service) handleSubscriptionUpdated(ctx context.Context, event *stripeapi.Event) error {
	subscriptionInfo, err := s.client.ParseSubscriptionFromEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to parse subscription from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(subscriptionInfo.OrganizationAddress.Hex())
	defer unlock()

	// Get organization
	orgAddress := subscriptionInfo.OrganizationAddress
	org, err := s.repository.Organization(orgAddress)
	if err != nil || org == nil {
		return NewStripeError("organization_not_found",
			fmt.Sprintf("organization %s not found for subscription %s",
				subscriptionInfo.OrganizationAddress, subscriptionInfo.ID), err)
	}

	// Handle different subscription statuses
	switch subscriptionInfo.Status {
	case "canceled":
		return s.handleSubscriptionCancellation(ctx, subscriptionInfo.ID, org)
	case "active":
		if !org.Subscription.Active {
			return s.handleSubscriptionActivation(ctx, subscriptionInfo.ID, org)
		}
	default:
		// No action needed for other statuses
	}

	log.Infof("stripe webhook: subscription %s updated to status %s for organization %s",
		subscriptionInfo.ID, subscriptionInfo.Status, subscriptionInfo.OrganizationAddress)
	return nil
}

// handleSubscriptionCancellation handles a canceled subscription by switching to the default plan
func (s *Service) handleSubscriptionCancellation(_ context.Context, subscriptionID string, org *db.Organization) error {
	// Get default plan
	defaultPlan, err := s.repository.DefaultPlan()
	if err != nil || defaultPlan == nil {
		return NewStripeError("plan_not_found", "default plan not found", err)
	}

	// Create subscription with default plan
	orgSubscription := &db.OrganizationSubscription{
		PlanID:          defaultPlan.ID,
		StartDate:       time.Now(),
		LastPaymentDate: org.Subscription.LastPaymentDate,
		Active:          true,
		MaxCensusSize:   defaultPlan.Organization.MaxCensus,
	}

	if err := s.repository.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to cancel subscription %s for organization %s",
				subscriptionID, org.Address.Hex()), err)
	}

	log.Infof("stripe webhook: subscription %s canceled for organization %s, switched to default plan",
		subscriptionID, org.Address.Hex())
	return nil
}

// handleSubscriptionActivation handles activating a subscription
func (s *Service) handleSubscriptionActivation(_ context.Context, subscriptionID string, org *db.Organization) error {
	org.Subscription.Active = true
	if err := s.repository.SetOrganization(org); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to activate subscription %s for organization %s",
				subscriptionID, org.Address.Hex()), err)
	}

	log.Infof("stripe webhook: subscription %s activated for organization %s",
		subscriptionID, org.Address.Hex())
	return nil
}

// handlePaymentSucceeded processes a successful payment event
func (s *Service) handlePaymentSucceeded(ctx context.Context, event *stripeapi.Event) error {
	invoiceInfo, err := s.client.ParseInvoiceFromEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to parse invoice from event: %w", err)
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(invoiceInfo.OrganizationAddress)
	defer unlock()

	// Get organization
	orgAddress := common.HexToAddress(invoiceInfo.OrganizationAddress)
	org, err := s.repository.Organization(orgAddress)
	if err != nil || org == nil {
		return NewStripeError("organization_not_found",
			fmt.Sprintf("organization %s not found for payment %s",
				invoiceInfo.OrganizationAddress, invoiceInfo.ID), err)
	}

	// Update last payment date
	org.Subscription.LastPaymentDate = invoiceInfo.PaymentTime
	if err := s.repository.SetOrganization(org); err != nil {
		return NewStripeError("database_error",
			fmt.Sprintf("failed to update payment date for organization %s",
				invoiceInfo.OrganizationAddress), err)
	}

	log.Infof("stripe webhook: payment %s processed for organization %s",
		invoiceInfo.ID, invoiceInfo.OrganizationAddress)
	return nil
}

// CreateCheckoutSession creates a new checkout session
func (s *Service) CreateCheckoutSession(ctx context.Context, params *CheckoutSessionParams) (*stripeapi.CheckoutSession, error) {
	return s.client.CreateCheckoutSession(ctx, params)
}

// GetCheckoutSession retrieves a checkout session status
func (s *Service) GetCheckoutSession(ctx context.Context, sessionID string) (*CheckoutSessionStatus, error) {
	return s.client.GetCheckoutSession(ctx, sessionID)
}

// CreatePortalSession creates a billing portal session
func (s *Service) CreatePortalSession(ctx context.Context, customerEmail string) (*stripeapi.BillingPortalSession, error) {
	return s.client.CreatePortalSession(ctx, customerEmail)
}

// GetPlansFromStripe retrieves plans from Stripe and converts them to database format
func (s *Service) GetPlansFromStripe(ctx context.Context) ([]*db.Plan, error) {
	var plans []*db.Plan
	productIDs := s.config.GetAllProductIDs()

	for i, productID := range productIDs {
		product, err := s.client.GetProduct(ctx, productID)
		if err != nil {
			return nil, fmt.Errorf("failed to get product %s: %w", productID, err)
		}

		plan, err := processProductToPlan(i, productID, product)
		if err != nil {
			return nil, fmt.Errorf("failed to process product %s: %w", productID, err)
		}

		plans = append(plans, plan)
	}

	return plans, nil
}

// processProductToPlan converts a Stripe product to a database plan
func processProductToPlan(index int, productID string, product *stripeapi.Product) (*db.Plan, error) {
	// This is a simplified version - in the full implementation, you'd parse metadata
	// For now, we'll create a basic plan structure
	price := product.DefaultPrice
	startingPrice := price.UnitAmount
	if len(price.Tiers) > 0 {
		startingPrice = price.Tiers[0].FlatAmount
	}

	return &db.Plan{
		ID:            uint64(index + 1),
		Name:          product.Name,
		StartingPrice: startingPrice,
		StripeID:      productID,
		StripePriceID: price.ID,
		Default:       price.Metadata["Default"] == "true",
		// Note: In the full implementation, you'd parse the metadata for:
		// Organization, VotingTypes, Features, CensusSizeTiers
	}, nil
}
