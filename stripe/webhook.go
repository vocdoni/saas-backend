package stripe

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// SubscriptionInfo represents the information related to a Stripe subscription
// that are relevant for the application.
type SubscriptionInfo struct {
	ID            string
	Status        stripeapi.SubscriptionStatus
	BillingPeriod db.BillingPeriod
	ProductID     string
	OrgAddress    common.Address
	Customer      *stripeapi.Customer
	StartDate     time.Time
	EndDate       time.Time
}

// InvoiceInfo represents invoice information extracted from events
type InvoiceInfo struct {
	ID          string
	PaymentTime time.Time
	OrgAddress  common.Address
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
	case stripeapi.EventTypeProductCreated,
		stripeapi.EventTypeProductUpdated:
		return s.handleProductUpsert(event)
	case stripeapi.EventTypeProductDeleted:
		return s.handleProductDelete(event)
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
	// Get plan by its ID (the Stripe product ID)
	plan, err := s.db.Plan(subscriptionInfo.ProductID)
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
		return fmt.Errorf("failed to save subscription %s (planID=%s, status=%s) for organization %s: %v",
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
	log.Infof("stripe webhook: subscription %s (planID=%s, status=%s) saved for organization %s",
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

// handleProductUpsert processes a product created/updated event, keeping the local plan cache
// in sync with Stripe (the source of truth). Any product carrying our plan-metadata marker is
// upserted; a product that lacks (or has lost) the marker is removed from the cache if present.
func (s *Service) handleProductUpsert(event *stripeapi.Event) error {
	product, err := parseProductFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse product from event: %w", err)
	}

	// Ignore products that are not vocdoni plans, and inactive products. If we previously had this product cached as a
	// plan (e.g. it was deactivated or its marker was removed), drop it so the cache doesn't go stale.
	if !product.Active || !isPlanProduct(product) {
		existing, err := s.db.Plan(product.ID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			// Not cached; nothing to drop.
		case err != nil:
			return fmt.Errorf("failed to look up plan for product %s: %w", product.ID, err)
		default:
			if err := s.db.DelPlan(existing); err != nil {
				return fmt.Errorf("failed to remove de-marked plan for product %s: %w", product.ID, err)
			}
			log.Infof("stripe webhook: product %s no longer a plan, removed from cache", product.ID)
		}
		return nil
	}

	// Webhook event payloads don't expand default_price, but isDefaultPlan reads
	// product.DefaultPrice.Metadata. Re-fetch the product with default_price expanded
	// (as the full sync does) so a product.updated event can't clear the Default flag.
	product, err = s.client.GetProduct(product.ID)
	if err != nil {
		return fmt.Errorf("failed to fetch product %s with expanded default price: %w", product.ID, err)
	}

	prices, err := s.client.GetProductPrices(product.ID)
	if err != nil {
		return fmt.Errorf("failed to get prices for product %s: %w", product.ID, err)
	}

	plan, err := processProductToPlan(product, prices)
	if err != nil {
		return fmt.Errorf("failed to process product %s: %w", product.ID, err)
	}

	if err := s.db.SetPlan(plan); err != nil {
		return fmt.Errorf("failed to upsert plan for product %s: %w", product.ID, err)
	}

	log.Infof("stripe webhook: product %s upserted, plan %s refreshed", product.ID, plan.ID)
	return nil
}

// handleProductDelete removes a plan from the local cache when its Stripe product is deleted.
func (s *Service) handleProductDelete(event *stripeapi.Event) error {
	product, err := parseProductFromEvent(event)
	if err != nil {
		return fmt.Errorf("failed to parse product from event: %w", err)
	}

	existing, err := s.db.Plan(product.ID)
	if errors.Is(err, db.ErrNotFound) {
		// Not a plan we track; nothing to do.
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up plan for product %s: %w", product.ID, err)
	}
	if err := s.db.DelPlan(existing); err != nil {
		return fmt.Errorf("failed to delete plan for product %s: %w", product.ID, err)
	}

	log.Infof("stripe webhook: product %s deleted, plan removed from cache", product.ID)
	return nil
}

// parseSubscriptionFromEvent extracts subscription information from a webhook event
func parseSubscriptionFromEvent(event *stripeapi.Event) (*SubscriptionInfo, error) {
	var subscription stripeapi.Subscription
	if err := json.Unmarshal(event.Data.Raw, &subscription); err != nil {
		return nil, fmt.Errorf("failed to parse subscription from event: %v", err)
	}

	orgAddress := common.HexToAddress(subscription.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, fmt.Errorf("subscription missing address metadata")
	}

	if len(subscription.Items.Data) == 0 {
		return nil, fmt.Errorf("subscription has no items")
	}

	subscriptionInfo := &SubscriptionInfo{
		ID:         subscription.ID,
		Status:     subscription.Status,
		ProductID:  subscription.Items.Data[0].Plan.Product.ID,
		OrgAddress: orgAddress,
		Customer:   subscription.Customer,
		StartDate:  time.Unix(subscription.Items.Data[0].CurrentPeriodStart, 0),
		EndDate:    time.Unix(subscription.Items.Data[0].CurrentPeriodEnd, 0),
	}

	if subscription.Items.Data[0].Price.Type == stripeapi.PriceTypeRecurring {
		subscriptionInfo.BillingPeriod = db.BillingPeriod((subscription.Items.Data[0].Price.Recurring.Interval))
	}

	return subscriptionInfo, nil
}

// parseInvoiceFromEvent extracts invoice information from a webhook event
func parseInvoiceFromEvent(event *stripeapi.Event) (*InvoiceInfo, error) {
	var invoice stripeapi.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return nil, fmt.Errorf("failed to parse invoice from event: %v", err)
	}

	if invoice.EffectiveAt == 0 {
		return nil, fmt.Errorf("invoice missing effective date")
	}

	if invoice.Parent.SubscriptionDetails == nil {
		return nil, fmt.Errorf("invoice missing subscription details")
	}
	orgAddress := common.HexToAddress(invoice.Parent.SubscriptionDetails.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, fmt.Errorf("invoice missing address metadata")
	}

	if invoice.Status != stripeapi.InvoiceStatusPaid {
		return nil, fmt.Errorf("invoice is not paid")
	}

	return &InvoiceInfo{
		ID:          invoice.ID,
		PaymentTime: time.Unix(invoice.EffectiveAt, 0),
		OrgAddress:  orgAddress,
	}, nil
}

// parseProductFromEvent extracts product information from a webhook event
func parseProductFromEvent(event *stripeapi.Event) (*stripeapi.Product, error) {
	var product stripeapi.Product
	if err := json.Unmarshal(event.Data.Raw, &product); err != nil {
		return nil, fmt.Errorf("failed to parse product from event: %v", err)
	}

	return &product, nil
}
