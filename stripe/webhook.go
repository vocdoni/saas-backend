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

// errPermanentEvent marks a webhook event that can never succeed: the metadata Stripe
// delivered is immutable, so every one of its retries — Stripe retries for about three
// days — would fail identically, burying real incidents in the endpoint's failure queue.
// Wrap it and HandleWebhookEvent logs the event and answers Stripe with a 200 instead.
var errPermanentEvent = fmt.Errorf("permanently invalid webhook event")

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
		if !errors.Is(err, errPermanentEvent) {
			return err
		}
		// terminal: a retry cannot fix it, so drop it here — and fall through to Store,
		// so a retry that arrives anyway short-circuits on the event id
		log.Errorw(err, fmt.Sprintf("stripe webhook: dropping permanently invalid event %s (%s)",
			event.ID, event.Type))
	}

	// Mark event as processed if successful
	now := time.Now()
	s.processedEvents.Store(event.ID, now)
	s.pruneProcessedEvents(now)

	return nil
}

// processedEventsRetention is how long a processed event id is remembered. Stripe stops retrying
// an event after three days, so older ids can no longer be redelivered.
const processedEventsRetention = 72 * time.Hour

// pruneProcessedEvents forgets the event ids processed longer than processedEventsRetention ago,
// so the dedup map does not grow for as long as the service runs.
func (s *Service) pruneProcessedEvents(now time.Time) {
	s.processedEvents.Range(func(id, processedAt any) bool {
		if t, ok := processedAt.(time.Time); ok && now.Sub(t) > processedEventsRetention {
			s.processedEvents.Delete(id)
		}
		return true
	})
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

// handleSubscription processes a subscription creation, update or deletion event. The event
// payload is only trusted to route the event — the subscription id and the organization it
// was created for: Stripe does not guarantee delivery order and the processed-events map is
// empty after a restart, so a stale redelivered event (e.g. an "active" update arriving after
// a cancellation) must never write old state. The subscription is re-fetched from the Stripe
// API and its live state is what gets acted upon — Stripe's recommended pattern for
// state-bearing webhook events. The fetch happens under the organization lock: fetched
// before it, two concurrent events of one subscription could save their states in the
// opposite order to the one they were read in, and the older one would win.
func (s *Service) handleSubscription(event *stripeapi.Event) error {
	eventInfo, err := parseSubscriptionEventRoute(event)
	if err != nil {
		return err
	}

	// Use per-organization locking
	unlock := s.lockManager.LockOrganization(eventInfo.OrgAddress)
	defer unlock()

	liveSubscription, err := s.client.GetSubscription(eventInfo.ID)
	if errors.Is(err, errSubscriptionNotFound) {
		// Stripe says it does not exist: no redelivery can change that
		return fmt.Errorf("failed to fetch subscription %s from stripe: %w: %w", eventInfo.ID, err, errPermanentEvent)
	}
	if err != nil {
		// transient (network, rate limit, outage): fail so Stripe redelivers the event
		return fmt.Errorf("failed to fetch subscription %s from stripe: %w", eventInfo.ID, err)
	}
	subscriptionInfo, err := newSubscriptionInfo(liveSubscription)
	if err != nil {
		// a later edit of the subscription emits its own customer.subscription.updated event,
		// so a retry of this one cannot be the event that fixes it
		return fmt.Errorf("live subscription %s: %w: %w", eventInfo.ID, err, errPermanentEvent)
	}
	if subscriptionInfo.OrgAddress != eventInfo.OrgAddress {
		// the address metadata was edited after this event: the update event of that edit
		// carries the new address and is handled under that organization's lock
		return fmt.Errorf("subscription %s now belongs to organization %s, not %s: %w",
			eventInfo.ID, subscriptionInfo.OrgAddress, eventInfo.OrgAddress, errPermanentEvent)
	}

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
		// only the organization's current subscription can downgrade it: after a plan change,
		// the cancellation of the replaced subscription may arrive after the new one is saved
		if current := org.Subscription.StripeSubscriptionID; current != "" && current != subscriptionInfo.ID {
			log.Warnw("stripe webhook: ignoring cancellation of a subscription that is not the current one",
				"subscription", subscriptionInfo.ID, "current", current, "org", org.Address)
			return nil
		}
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

	// only the subscription fields this event owns: lastPaymentDate is the invoice
	// webhook's, and the rest of the organization is the API's
	if err := s.db.UpdateOrganizationSubscription(org.Address, &db.OrganizationSubscription{
		PlanID:               plan.ID,
		StripeSubscriptionID: subscriptionInfo.ID,
		BillingPeriod:        subscriptionInfo.BillingPeriod,
		StartDate:            subscriptionInfo.StartDate,
		RenewalDate:          subscriptionInfo.EndDate,
		Active:               true,
		Email:                subscriptionInfo.Customer.Email,
	}); err != nil {
		return fmt.Errorf("failed to save subscription %s (planID=%s, status=%s) for organization %s: %v",
			subscriptionInfo.ID, plan.ID, subscriptionInfo.Status, subscriptionInfo.OrgAddress, err)
	}

	// Stamp the customer with the organization address the first time; a customer already
	// stamped for another organization is left untouched, since the billing portal resolves
	// the customer from that address
	if stamped := subscriptionInfo.Customer.Metadata["address"]; stamped == "" {
		if err := s.client.UpdateCustomerMetadata(
			subscriptionInfo.Customer.ID,
			map[string]string{"address": subscriptionInfo.OrgAddress.String()},
		); err != nil {
			log.Warnf("stripe webhook: failed to update customer %s metadata: %v",
				subscriptionInfo.Customer.ID, err)
		}
	} else if common.HexToAddress(stamped) != subscriptionInfo.OrgAddress {
		log.Warnw("stripe webhook: customer is stamped with another organization address",
			"customer", subscriptionInfo.Customer.ID, "stamped", stamped, "org", subscriptionInfo.OrgAddress)
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

	// Switch to the default plan, keeping the last payment date (it decides trial eligibility)
	orgSubscription := &db.OrganizationSubscription{
		PlanID:               defaultPlan.ID,
		StripeSubscriptionID: "",
		StartDate:            time.Now(),
		Active:               true,
	}

	if err := s.db.UpdateOrganizationSubscription(org.Address, orgSubscription); err != nil {
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

	// Update last payment date
	err = s.db.SetOrganizationLastPaymentDate(invoiceInfo.OrgAddress, invoiceInfo.PaymentTime)
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("organization %s not found for payment %s: %w",
			invoiceInfo.OrgAddress, invoiceInfo.ID, err)
	}
	if err != nil {
		return fmt.Errorf("failed to update payment date for organization %s: %w",
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

// subscriptionEventRoute is what a subscription webhook event is trusted for: which
// subscription it is about and which organization it was created for.
type subscriptionEventRoute struct {
	ID         string
	OrgAddress common.Address
}

// parseSubscriptionEventRoute extracts the subscription id and organization address from a
// subscription webhook event. The rest of the payload is ignored: it may be stale. The
// payload of an event never changes, so a missing id or address is permanent.
func parseSubscriptionEventRoute(event *stripeapi.Event) (*subscriptionEventRoute, error) {
	var subscription stripeapi.Subscription
	if err := json.Unmarshal(event.Data.Raw, &subscription); err != nil {
		return nil, fmt.Errorf("failed to parse subscription from event %s: %v: %w", event.ID, err, errPermanentEvent)
	}
	if subscription.ID == "" {
		return nil, fmt.Errorf("subscription event %s has no subscription id: %w", event.ID, errPermanentEvent)
	}
	orgAddress := common.HexToAddress(subscription.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, fmt.Errorf("subscription %s missing address metadata: %w", subscription.ID, errPermanentEvent)
	}
	return &subscriptionEventRoute{ID: subscription.ID, OrgAddress: orgAddress}, nil
}

// newSubscriptionInfo maps a Stripe subscription onto the fields the application acts on,
// validating that everything it needs (org address metadata, customer, plan, price) is there.
func newSubscriptionInfo(subscription *stripeapi.Subscription) (*SubscriptionInfo, error) {
	orgAddress := common.HexToAddress(subscription.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, fmt.Errorf("subscription missing address metadata")
	}

	if subscription.Items == nil || len(subscription.Items.Data) == 0 {
		return nil, fmt.Errorf("subscription has no items")
	}
	item := subscription.Items.Data[0]
	if subscription.Customer == nil || item.Plan == nil || item.Plan.Product == nil || item.Price == nil {
		return nil, fmt.Errorf("subscription %s is missing its customer, plan or price", subscription.ID)
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

	if item.Price.Type == stripeapi.PriceTypeRecurring && item.Price.Recurring != nil {
		subscriptionInfo.BillingPeriod = db.BillingPeriod(subscription.Items.Data[0].Price.Recurring.Interval)
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
