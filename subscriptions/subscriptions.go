package subscriptions

import (
	"fmt"

	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/proto/build/go/models"
)

// SubscriptionsConfig holds the configuration for the subscriptions service.
// It includes a reference to the MongoDB storage used by the service.
type SubscriptionsConfig struct {
	DB *db.MongoStorage
}

// Subscriptions is the service that manages the organization permissions based on
// the subscription plans.
type Subscriptions struct {
	db *db.MongoStorage
}

// New creates a new Subscriptions service with the given configuration.
func New(conf *SubscriptionsConfig) *Subscriptions {
	if conf == nil {
		return nil
	}
	return &Subscriptions{
		db: conf.DB,
	}
}

// HasPermission checks if the organization has permission to perform the given transaction.
func (p *Subscriptions) HasPermission(
	tx *models.Tx,
	txType models.TxType,
	org *db.Organization,
) (bool, error) {
	// get subscription plan
	// plan, err := p.db.Subscription(org.Subscription.SubscriptionID)
	// if err != nil {
	// 	return false, fmt.Errorf("could not get organization subscription: %v", err)
	// }
	switch txType {
	case models.TxType_NEW_PROCESS, models.TxType_SET_PROCESS_CENSUS:
		newProcess := tx.GetNewProcess()
		if newProcess.Process.MaxCensusSize > uint64(org.Subscription.MaxCensusSize) {
			return false, fmt.Errorf("MaxCensusSize is greater than the allowed")
		}
	}
	return true, nil
}
