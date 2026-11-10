// Package stripe provides integration with the Stripe payment service: one-time checkouts that
// top up organization wallets, and the webhook events that credit, refund or dispute them.
package stripe

import (
	"fmt"
	"sync"

	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Service provides the main business logic for Stripe operations
type Service struct {
	client          *Client
	db              *db.MongoStorage
	processedEvents sync.Map // map[string]time.Time
	config          *Config
	// OnProcessTopUp, when set, is invoked after a top-up opened for a process is credited, on
	// every delivery including Stripe's replays, so it must be idempotent. The API layer wires
	// it to pay the process's publish (or census headroom) from the wallet. An error makes the
	// webhook answer 500 so Stripe redelivers the event; a refusal no retry can fix returns nil.
	OnProcessTopUp func(ProcessTopUp) error
	// OnProcessCheckoutDropped, when set, is invoked when a process's top-up checkout expires or
	// its delayed payment fails, to drop the process's pending payment and its add-on claims.
	OnProcessCheckoutDropped func(processID bson.ObjectID, sessionID string) error
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
		client: NewClient(config),
		db:     database,
		config: config,
	}, nil
}

// GetCheckoutSession retrieves a checkout session status. A session Stripe does not know
// answers errors.ErrCheckoutSessionNotFound.
func (s *Service) GetCheckoutSession(sessionID string) (*CheckoutSessionStatus, error) {
	return s.client.GetCheckoutSession(sessionID)
}
