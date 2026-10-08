package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/util"
)

var mockCustomer = &stripeapi.Customer{
	ID:    "cus_test123",
	Email: "test@example.com",
}

func newStripeService(t *testing.T) *stripe.Service {
	t.Helper()
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

// stubStripeSubscriptionBackend points the Stripe client at a stub that serves the live
// state of the given subscriptions (by ID), plus the mock customer. The webhook handler
// re-fetches a subscription from the API before acting, so tests register the live state
// here and deliver events that may agree with it or be stale.
func stubStripeSubscriptionBackend(t *testing.T, subs map[string]*stripeapi.Subscription) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
			sub, ok := subs[strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(sub)
		case strings.HasPrefix(r.URL.Path, "/v1/customers/"):
			_ = json.NewEncoder(w).Encode(mockCustomer)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

func TestStripeWebhook(t *testing.T) {
	c := qt.New(t)

	// Create test organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	service := newStripeService(t)

	// live subscription state the webhook handler re-fetches before acting
	liveSubs := map[string]*stripeapi.Subscription{}
	stubStripeSubscriptionBackend(t, liveSubs)

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
		essential := mockStripeSubscription(orgAddress, mockEssentialPlan.ID)
		liveSubs[essential.ID] = essential
		{
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionCreated, essential))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should have changed plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, mockEssentialPlan.ID)
		}

		// Mock a subscription upgrade: a new subscription replaces the essential one
		premium := mockStripeSubscription(orgAddress, mockPremiumPlan.ID)
		liveSubs[premium.ID] = premium
		{
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionUpdated, premium))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should have changed plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, mockPremiumPlan.ID)
		}

		// The replaced (essential) subscription's late cancellation must be ignored:
		// it is no longer the subscription the organization is on
		essential.Status = stripeapi.SubscriptionStatusCanceled
		essential.CanceledAt = time.Now().Unix()
		{
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionDeleted, essential))
			c.Assert(err, qt.IsNil)
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.PlanID, qt.Equals, mockPremiumPlan.ID)
		}

		// A stale redelivered "active" update must not resurrect the paid plan once the
		// subscription is canceled in Stripe: the handler acts on the live (canceled) state
		stale := *premium
		stale.Status = stripeapi.SubscriptionStatusActive
		premium.Status = stripeapi.SubscriptionStatusCanceled
		premium.CanceledAt = time.Now().Unix()
		{
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionUpdated, &stale))
			c.Assert(err, qt.IsNil)
		}

		// Get organization from database again, should be back on the default plan
		{
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.Active, qt.IsTrue)
			c.Assert(org.Subscription.PlanID, qt.Equals, defaultPlan.ID)
		}

		// The actual deletion event arriving afterwards is an idempotent no-op
		{
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionDeleted, premium))
			c.Assert(err, qt.IsNil)
			org, err := testDB.Organization(orgAddress)
			c.Assert(err, qt.IsNil)
			c.Assert(org.Subscription.PlanID, qt.Equals, defaultPlan.ID)
		}
	})

	t.Run("SubscriptionErrors", func(*testing.T) {
		{
			s := mockStripeSubscription(orgAddress, mockEssentialPlan.ID)
			s.Metadata = nil
			err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeCustomerSubscriptionCreated, s))
			c.Assert(err, qt.ErrorMatches, ".* subscription missing address metadata")
		}

		{
			s := mockStripeSubscription(orgAddress, mockEssentialPlan.ID)
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

		// an invoice that is not a subscription's (a one-time payment's receipt has no parent)
		// is skipped, not refused: an error would only make Stripe retry it for days
		{
			s := mockStripeInvoicePayment(orgAddress, date)
			s.Parent.SubscriptionDetails = nil
			c.Assert(service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s)), qt.IsNil)
			s.Parent = nil
			c.Assert(service.HandleEvent(mockStripeEvent(stripeapi.EventTypeInvoicePaymentSucceeded, s)), qt.IsNil)
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
	// 		plan, err := testDB.Plan(mockEssentialPlan.ID)
	// 		c.Assert(err, qt.IsNil)
	// 		c.Assert(plan.Name, qt.Equals, mockEssentialPlan.Name)
	// 		c.Assert(plan.MonthlyPrice, qt.Equals, mockEssentialPlan.MonthlyPrice)
	// 		c.Assert(plan.YearlyPrice, qt.Equals, mockEssentialPlan.YearlyPrice)
	// 		c.Assert(plan.Features.Personalization, qt.Equals, mockEssentialPlan.Features.Personalization)
	// 	}

	// 	{
	// 		s := mockStripeProduct(mockEssentialPlan.ID)
	// 		s.Name = "New Name"
	// 		s.DefaultPrice.UnitAmount = mockEssentialPlan.MonthlyPrice + 1000 // TODO: FIX
	// 		s.Metadata["features"] = `{"personalization": false, "emailReminder": true, "smsNotification": false}`
	// 		err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeProductUpdated, s))
	// 		c.Assert(err, qt.IsNil)
	// 	}

	// 	{
	// 		plan, err := testDB.Plan(mockEssentialPlan.ID)
	// 		c.Assert(err, qt.IsNil)
	// 		c.Assert(plan.Name, qt.Equals, "New Name")
	// 		c.Assert(plan.MonthlyPrice, qt.Equals, mockEssentialPlan.MonthlyPrice+1000) // TODO: FIX
	// 		c.Assert(plan.YearlyPrice, qt.Equals, mockEssentialPlan.YearlyPrice)
	// 		c.Assert(plan.Features.Personalization, qt.IsFalse)
	// 	}
	// })

	// t.Run("ProductErrors", func(*testing.T) {
	// 	{
	// 		s := mockStripeProduct(mockEssentialPlan.ID)
	// 		s.Metadata["features"] = `invalid-json`
	// 		err := service.HandleEvent(mockStripeEvent(stripeapi.EventTypeProductUpdated, s))
	// 		c.Assert(err, qt.ErrorMatches, ".* error parsing plan metadata JSON.*")
	// 	}
	// })
}
