// FILEPATH: /home/user/Projects/vocdoni/saas-backend/db/subscriptions_test.go

package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestSetSubscription(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)

	subscription := &Subscription{
		Name:     "Test Subscription",
		StripeID: "stripeID",
	}
	_, err := db.SetSubscription(subscription)
	c.Assert(err, qt.IsNil)
}

func TestSubscription(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t) // Create a new quicktest instance
	subscriptionID := uint64(123)
	// Test not found subscription
	subscription, err := db.Subscription(subscriptionID)
	c.Assert(err, qt.Equals, ErrNotFound)
	c.Assert(subscription, qt.IsNil)
	subscription = &Subscription{
		Name:     "Test Subscription",
		StripeID: "stripeID",
	}
	subscriptionID, err = db.SetSubscription(subscription)
	if err != nil {
		t.Error(err)
	}
	// Test found subscription
	subscriptionDB, err := db.Subscription(subscriptionID)
	c.Assert(err, qt.IsNil)
	c.Assert(subscriptionDB, qt.Not(qt.IsNil))
	c.Assert(subscriptionDB.ID, qt.Equals, subscription.ID)
}

func TestDelSubscription(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// Create a new subscription and delete it
	subscription := &Subscription{
		Name:     "Test Subscription",
		StripeID: "stripeID",
	}
	id, err := db.SetSubscription(subscription)
	c.Assert(err, qt.IsNil)
	err = db.DelSubscription(subscription)
	c.Assert(err, qt.IsNil)

	// Test not found subscription
	_, err = db.Subscription(id)
	c.Assert(err, qt.Equals, ErrNotFound)
}
