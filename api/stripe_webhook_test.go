package api

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/util"
)

var mockCustomer = &stripeapi.Customer{
	ID:    "cus_test123",
	Email: "test@example.com",
}

func newStripeService(t *testing.T) *stripe.Service {
	c := qt.New(t)

	_ = os.Setenv("VOCDONI_STRIPEAPISECRET", "mockAPISecret")
	_ = os.Setenv("VOCDONI_STRIPEWEBHOOKSECRET", "mockWebhookSecret")

	// Create Stripe handlers
	config, err := stripe.NewConfig()
	c.Assert(err, qt.IsNil)
	service, err := stripe.NewService(config, testDB)
	c.Assert(err, qt.IsNil)
	return service
}

// mockStripeEvent creates a mock Stripe event with a random ID
func mockStripeEvent(eventType stripeapi.EventType, event any) *stripeapi.Event {
	rawData, _ := json.Marshal(event)

	return &stripeapi.Event{
		ID:   util.RandomHex(16),
		Type: eventType,
		Data: &stripeapi.EventData{
			Raw: rawData,
		},
	}
}

func mockStripeSubscription(orgAddress common.Address, productID string) *stripeapi.Subscription {
	return &stripeapi.Subscription{
		ID:     util.RandomHex(16),
		Object: "subscription",
		Status: stripeapi.SubscriptionStatusActive,
		Customer: &stripeapi.Customer{
			ID:    mockCustomer.ID,
			Email: mockCustomer.Email,
		},
		Metadata: map[string]string{
			"address": orgAddress.String(),
		},
		Items: &stripeapi.SubscriptionItemList{
			Data: []*stripeapi.SubscriptionItem{
				{
					Price: &stripeapi.Price{
						Type: stripeapi.PriceTypeRecurring,
						Recurring: &stripeapi.PriceRecurring{
							Interval: stripeapi.PriceRecurringIntervalYear,
						},
					},
					CurrentPeriodStart: time.Now().Unix(),
					CurrentPeriodEnd:   time.Now().Add(30 * 24 * time.Hour).Unix(),
					Quantity:           1,
					Plan: &stripeapi.Plan{
						Product: mockStripeProduct(productID),
					},
				},
			},
		},
	}
}

func mockStripeProduct(productID string) *stripeapi.Product {
	return &stripeapi.Product{
		ID:     productID,
		Object: "product",
		Name:   "Vocdoni Plan" + productID,
		Active: true,
		DefaultPrice: &stripeapi.Price{
			ID:         "mock_price_id",
			UnitAmount: 2999,
			Metadata: map[string]string{
				"Default": "false",
			},
		},
		Metadata: map[string]string{
			"organization": `{"maxCensus": 2000, "maxProcesses": 50}`,
			"votingTypes":  `{"approval": true, "ranked": true, "weighted": false}`,
			"features":     `{"personalization": true, "emailReminder": true, "smsNotification": false}`,
		},
	}
}

func mockStripeInvoicePayment(orgAddress common.Address, date time.Time) *stripeapi.Invoice {
	return &stripeapi.Invoice{
		ID:          util.RandomHex(16),
		Status:      stripeapi.InvoiceStatusPaid,
		EffectiveAt: date.Unix(),
		Parent: &stripeapi.InvoiceParent{
			Type: stripeapi.InvoiceParentTypeSubscriptionDetails,
			SubscriptionDetails: &stripeapi.InvoiceParentSubscriptionDetails{
				Metadata: map[string]string{
					"address": orgAddress.String(),
				},
			},
		},
	}
}

func TestStripeWebhook(t *testing.T) {
	c := qt.New(t)

	// Create test organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	service := newStripeService(t)

	t.Run("SubscriptionCreateUpgradeAndCancel", func(*testing.T) {
		// Get default plan details
		defaultPlan, err := testDB.DefaultPlan()
		c.Assert(err, qt.IsNil)
		c.Assert(defaultPlan.ID, qt.Not(qt.Equals), mockEssentialPlan.ID)

		// Get organization from database, should have default plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, defaultPlan.ID)
		}

		// Mock a new subscription
		{
			s := mockStripeSubscription(orgAddress, mockEssentialPlan.StripeID)
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionCreated, s))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should have changed plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, mockEssentialPlan.ID)
		}

		// Mock a subscription upgrade
		{
			s := mockStripeSubscription(orgAddress, mockPremiumPlan.StripeID)
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionUpdated, s))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should have changed plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, mockPremiumPlan.ID)
		}

		// Cancel subscription
		{
			s := mockStripeSubscription(orgAddress, mockPremiumPlan.StripeID)
			s.Status = stripeapi.SubscriptionStatusCanceled
			s.CanceledAt = time.Now().Unix()
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionDeleted, s))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should have changed plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, defaultPlan.ID)
		}
	})

	t.Run("SubscriptionErrors", func(*testing.T) {
		{
			s := mockStripeSubscription(orgAddress, mockEssentialPlan.StripeID)
			s.Metadata = nil
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionCreated, s))
			c.Assert(err, qt.ErrorMatches, ".* subscription missing address metadata")
		}

		{
			s := mockStripeSubscription(orgAddress, mockEssentialPlan.StripeID)
			s.Items.Data = nil
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionCreated, s))
			c.Assert(err, qt.ErrorMatches, ".* subscription has no items")
		}
	})

	t.Run("InvoicePaymentSucceeded", func(*testing.T) {
		date := time.Now()

		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.LastPaymentDate.Unix(), qt.Not(qt.Equals), date.Unix())
		}

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.IsNil)
		}

		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.LastPaymentDate.Unix(), qt.Equals, date.Unix())
		}
	})

	t.Run("InvoiceErrors", func(*testing.T) {
		date := time.Now()

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.EffectiveAt = 0
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.ErrorMatches, ".* invoice missing effective date")
		}

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.Parent.SubscriptionDetails = nil
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.ErrorMatches, ".* invoice missing subscription details")
		}

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.Parent.SubscriptionDetails.Metadata["address"] = ""
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.ErrorMatches, ".* invoice missing address metadata")
		}

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.Parent.SubscriptionDetails.Metadata["address"] = "0x00dead"
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.ErrorMatches, "organization .* not found for payment .*")
		}

		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.Status = stripeapi.InvoiceStatusOpen
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s))
			c.Assert(err, qt.ErrorMatches, ".*invoice is not paid")
		}
	})

	// TODO: needs refactoring, can't fetch stripeapi prices with mock API key
	// t.Run("ProductUpdated", func(*testing.T) {
	// 	{
	// 		plan, err := testDB.PlanByStripeID(mockEssentialPlan.StripeID)
	// 		c.Assert(err, qt.IsNil)
	// 		c.Assert(plan.Name, qt.Equals, mockEssentialPlan.Name)
	// 		c.Assert(plan.MonthlyPrice, qt.Equals, mockEssentialPlan.MonthlyPrice)
	// 		c.Assert(plan.YearlyPrice, qt.Equals, mockEssentialPlan.YearlyPrice)
	// 		c.Assert(plan.Features.Personalization, qt.Equals, mockEssentialPlan.Features.Personalization)
	// 	}

	// 	{
	// 		s := mockStripeProduct(mockEssentialPlan.StripeID)
	// 		s.Name = "New Name"
	// 		s.DefaultPrice.UnitAmount = mockEssentialPlan.MonthlyPrice + 1000 // TODO: FIX
	// 		s.Metadata["features"] = `{"personalization": false, "emailReminder": true, "smsNotification": false}`
	// 		err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeProductUpdated, s))
	// 		c.Assert(err, qt.IsNil)
	// 	}

	// 	{
	// 		plan, err := testDB.PlanByStripeID(mockEssentialPlan.StripeID)
	// 		c.Assert(err, qt.IsNil)
	// 		c.Assert(plan.Name, qt.Equals, "New Name")
	// 		c.Assert(plan.MonthlyPrice, qt.Equals, mockEssentialPlan.MonthlyPrice+1000) // TODO: FIX
	// 		c.Assert(plan.YearlyPrice, qt.Equals, mockEssentialPlan.YearlyPrice)
	// 		c.Assert(plan.Features.Personalization, qt.IsFalse)
	// 	}
	// })

	// t.Run("ProductErrors", func(*testing.T) {
	// 	{
	// 		s := mockStripeProduct(mockEssentialPlan.StripeID)
	// 		s.Metadata["features"] = `invalid-json`
	// 		err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeProductUpdated, s))
	// 		c.Assert(err, qt.ErrorMatches, ".* error parsing plan metadata JSON.*")
	// 	}
	// })
}
