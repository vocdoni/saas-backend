package stripe

import (
	"errors"
	"fmt"
	"time"

	stripeapi "github.com/stripe/stripe-go/v87"
	"go.vocdoni.io/dvote/log"
)

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
	// a manual top-up invoice paid (through Stripe or marked paid out of band): credit its wallet;
	// every other invoice event of the retired subscription model stays ignored
	case stripeapi.EventTypeInvoicePaid:
		return s.handleInvoicePaid(event)
	case stripeapi.EventTypeCheckoutSessionCompleted,
		stripeapi.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		return s.handleCheckoutSessionResult(event)
	case stripeapi.EventTypeCheckoutSessionAsyncPaymentFailed,
		stripeapi.EventTypeCheckoutSessionExpired:
		return s.handleCheckoutSessionFailed(event)
	// a refund made in the dashboard: created succeeded for cards, or updated once it settles
	case stripeapi.EventTypeRefundCreated, stripeapi.EventTypeRefundUpdated:
		return s.handleRefund(event)
	// a dispute (chargeback) on a top-up: funds withdrawn, or reinstated after it was won
	case stripeapi.EventTypeChargeDisputeFundsWithdrawn, stripeapi.EventTypeChargeDisputeFundsReinstated:
		return s.handleDispute(event)
	default:
		log.Debugf("stripe webhook: received unhandled event type %s (id %s)", event.Type, event.ID)
		return nil
	}
}
