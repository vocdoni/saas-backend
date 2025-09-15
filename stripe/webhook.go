package stripe

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v81"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// SubscriptionInfo represents the information related to a Stripe subscription
// that are relevant for the application.
type SubscriptionInfo struct {
	ID            string
	Status        stripeapi.SubscriptionStatus
	ProductID     string
	OrgAddress    common.Address
	CustomerEmail string
	StartDate     time.Time
	EndDate       time.Time
}

// InvoiceInfo represents invoice information extracted from events
type InvoiceInfo struct {
	ID          string
	PaymentTime time.Time
	OrgAddress  common.Address
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
	org.Subscription.StartDate = subscriptionInfo.StartDate
	org.Subscription.RenewalDate = subscriptionInfo.EndDate
	org.Subscription.Active = (subscriptionInfo.Status == stripeapi.SubscriptionStatusActive)
	org.Subscription.Email = subscriptionInfo.CustomerEmail

	// Save subscription
	if err := s.db.SetOrganization(org); err != nil {
		return fmt.Errorf("failed to save subscription %s (planID=%d, status=%s) for organization %s: %v",
			subscriptionInfo.ID, plan.ID, subscriptionInfo.Status, subscriptionInfo.OrgAddress, err)
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
		PlanID:          defaultPlan.ID,
		StartDate:       time.Now(),
		LastPaymentDate: org.Subscription.LastPaymentDate,
		Active:          true,
	}

	if err := s.db.SetOrganizationSubscription(org.Address, orgSubscription); err != nil {
		return fmt.Errorf("failed to cancel subscription %s for organization %s: %v",
			subscriptionID, org.Address, err)
	}

	log.Infof("stripe webhook: subscription %s canceled for organization %s, switched to default plan",
		subscriptionID, org.Address)
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

	// Update the plan with new product information
	updatedPlan, err := processProductToPlan(existingPlan.ID, product)
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

// parseSubscriptionFromEvent extracts subscription information from a webhook event
func parseSubscriptionFromEvent(event *stripeapi.Event) (*SubscriptionInfo, error) {
	var subscription stripeapi.Subscription
	if err := json.Unmarshal(event.Data.Raw, &subscription); err != nil {
		return nil, errors.ErrMalformedBody.Withf("failed to parse subscription from event: %v", err)
	}

	orgAddress := common.HexToAddress(subscription.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, errors.ErrInvalidData.Withf("subscription missing address metadata")
	}

	if len(subscription.Items.Data) == 0 {
		return nil, errors.ErrInvalidData.Withf("subscription has no items")
	}

	return &SubscriptionInfo{
		ID:            subscription.ID,
		Status:        subscription.Status,
		ProductID:     subscription.Items.Data[0].Plan.Product.ID,
		OrgAddress:    orgAddress,
		CustomerEmail: subscription.Customer.Email,
		StartDate:     time.Unix(subscription.CurrentPeriodStart, 0),
		EndDate:       time.Unix(subscription.CurrentPeriodEnd, 0),
	}, nil
}

// parseInvoiceFromEvent extracts invoice information from a webhook event
func parseInvoiceFromEvent(event *stripeapi.Event) (*InvoiceInfo, error) {
	var invoice stripeapi.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return nil, errors.ErrMalformedBody.Withf("failed to parse invoice from event: %v", err)
	}

	if invoice.EffectiveAt == 0 {
		return nil, errors.ErrInvalidData.Withf("invoice missing effective date")
	}

	if invoice.SubscriptionDetails == nil {
		return nil, errors.ErrInvalidData.Withf("invoice missing subscription details")
	}
	orgAddress := common.HexToAddress(invoice.SubscriptionDetails.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, errors.ErrInvalidData.Withf("invoice missing address metadata")
	}

	if invoice.Status != stripeapi.InvoiceStatusPaid {
		return nil, errors.ErrInvalidData.Withf("invoice is not paid")
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
		return nil, errors.ErrMalformedBody.Withf("failed to parse product from event: %v", err)
	}

	return &product, nil
}
