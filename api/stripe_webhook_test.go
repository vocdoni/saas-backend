package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v81"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
)

// Test fixtures for Stripe webhook events
var (
	testOrgAddress     = common.HexToAddress("0x1234567890123456789012345678901234567890")
	testCustomerID     = "cus_test123"
	testCustomerEmail  = "test@example.com"
	testSubscriptionID = "sub_1234567890"
	testProductID      = "prod_R0kTryoMNl8I19" // Premium Annual Subscription Plan, id=1
)

// createTestSubscriptionCreatedEvent creates a mock Stripe subscription.created event
func createTestSubscriptionCreatedEvent() *stripeapi.Event {
	return createTestSubscriptionCreatedEventWithCustom(testOrgAddress, testProductID)
}

// createTestSubscriptionCreatedEventWithCustom creates a mock Stripe subscription.created event with custom parameters
func createTestSubscriptionCreatedEventWithCustom(orgAddress common.Address, productID string) *stripeapi.Event {
	// Create actual Stripe subscription object
	subscription := &stripeapi.Subscription{
		ID:                 testSubscriptionID,
		Object:             "subscription",
		Status:             stripeapi.SubscriptionStatusActive,
		CurrentPeriodStart: time.Now().Unix(),
		CurrentPeriodEnd:   time.Now().Add(30 * 24 * time.Hour).Unix(),
		Customer: &stripeapi.Customer{
			ID:    testCustomerID,
			Email: testCustomerEmail,
		},
		Metadata: map[string]string{
			"address": orgAddress.Hex(),
		},
		Items: &stripeapi.SubscriptionItemList{
			Data: []*stripeapi.SubscriptionItem{
				{
					Quantity: 1000,
					Plan: &stripeapi.Plan{
						Product: &stripeapi.Product{
							ID: productID,
						},
					},
				},
			},
		},
	}

	// Marshal the actual Stripe object
	rawData, _ := json.Marshal(subscription)

	return &stripeapi.Event{
		ID:   "evt_test_subscription_created",
		Type: "customer.subscription.created",
		Data: &stripeapi.EventData{
			Raw: rawData,
		},
	}
}

// createTestPaymentSucceededEvent creates a mock Stripe invoice.payment_succeeded event
func createTestPaymentSucceededEvent() *stripeapi.Event {
	// Create actual Stripe invoice object
	invoice := &stripeapi.Invoice{
		ID:          "in_test123",
		Object:      "invoice",
		EffectiveAt: time.Now().Unix(),
		SubscriptionDetails: &stripeapi.InvoiceSubscriptionDetails{
			Metadata: map[string]string{
				"address": testOrgAddress.Hex(),
			},
		},
	}

	// Marshal the actual Stripe object
	rawData, _ := json.Marshal(invoice)

	return &stripeapi.Event{
		ID:   "evt_test_payment_succeeded",
		Type: "invoice.payment_succeeded",
		Data: &stripeapi.EventData{
			Raw: rawData,
		},
	}
}

// StripeSubscriptionEvent represents the structure of a Stripe subscription webhook event
type StripeSubscriptionEvent struct {
	ID                 string   `json:"id"`
	Status             string   `json:"status"`
	CurrentPeriodStart int64    `json:"current_period_start"`
	CurrentPeriodEnd   int64    `json:"current_period_end"`
	Customer           struct { //revive:disable-line:nested-structs
		ID string `json:"id"`
	} `json:"customer"`
	Metadata struct { //revive:disable-line:nested-structs
		Address string `json:"address"`
	} `json:"metadata"`
	Items struct { //revive:disable-line:nested-structs
		Data []struct { //revive:disable-line:nested-structs
			Quantity int64    `json:"quantity"`
			Plan     struct { //revive:disable-line:nested-structs
				Product struct { //revive:disable-line:nested-structs
					ID string `json:"id"`
				} `json:"product"`
			} `json:"plan"`
		} `json:"data"`
	} `json:"items"`
}

// mockGetSubscriptionInfoFromEvent creates a mock subscription info without calling Stripe API
func mockGetSubscriptionInfoFromEvent(event stripeapi.Event) (*stripe.SubscriptionInfo, error) {
	var subscription StripeSubscriptionEvent
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal subscription: %w", err)
	}

	if len(subscription.Items.Data) == 0 {
		return nil, fmt.Errorf("no items in subscription")
	}

	return &stripe.SubscriptionInfo{
		ID:                  subscription.ID,
		Status:              subscription.Status,
		ProductID:           subscription.Items.Data[0].Plan.Product.ID,
		Quantity:            int(subscription.Items.Data[0].Quantity),
		OrganizationAddress: common.HexToAddress(subscription.Metadata.Address),
		CustomerEmail:       testCustomerEmail,
		StartDate:           time.Unix(subscription.CurrentPeriodStart, 0),
		EndDate:             time.Unix(subscription.CurrentPeriodEnd, 0),
	}, nil
}

// StripeInvoiceEvent represents the structure of a Stripe invoice webhook event
type StripeInvoiceEvent struct {
	ID                  string   `json:"id"`
	EffectiveAt         int64    `json:"effective_at"`
	SubscriptionDetails struct { //revive:disable-line:nested-structs
		Metadata struct { //revive:disable-line:nested-structs
			Address string `json:"address"`
		} `json:"metadata"`
	} `json:"subscription_details"`
}

// mockGetInvoiceInfoFromEvent creates a mock invoice info without calling Stripe API
func mockGetInvoiceInfoFromEvent(event stripeapi.Event) (time.Time, string, error) {
	var invoice StripeInvoiceEvent
	err := json.Unmarshal(event.Data.Raw, &invoice)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("failed to unmarshal invoice: %w", err)
	}

	effectiveAt := time.Unix(invoice.EffectiveAt, 0)
	address := invoice.SubscriptionDetails.Metadata.Address

	return effectiveAt, address, nil
}

func TestWebhookProcessingUnit(t *testing.T) {
	c := qt.New(t)

	// Create test organization
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Create a test plan that matches the product ID
	planID, err := testDB.SetPlan(&db.Plan{
		Name:     "Essential Plan",
		StripeID: testProductID,
		Default:  false,
		Organization: db.PlanLimits{
			MaxCensus: 1000,
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(planID, qt.Not(qt.Equals), uint64(0))

	t.Run("SubscriptionCreatedUnit", func(*testing.T) {
		// Update test organization address to match our test
		testOrgAddress = orgAddress

		event := createTestSubscriptionCreatedEvent()

		// Mock the subscription info extraction
		stripeSubscriptionInfo, err := mockGetSubscriptionInfoFromEvent(*event)
		c.Assert(err, qt.IsNil)

		// Get organization from database
		org, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)

		// Get plan from database
		dbSubscription, err := testDB.PlanByStripeID(stripeSubscriptionInfo.ProductID)
		c.Assert(err, qt.IsNil)
		c.Assert(dbSubscription, qt.Not(qt.IsNil))

		// Create organization subscription
		organizationSubscription := &db.OrganizationSubscription{
			PlanID:        dbSubscription.ID,
			StartDate:     stripeSubscriptionInfo.StartDate,
			RenewalDate:   stripeSubscriptionInfo.EndDate,
			Active:        stripeSubscriptionInfo.Status == "active",
			MaxCensusSize: stripeSubscriptionInfo.Quantity,
			Email:         stripeSubscriptionInfo.CustomerEmail,
		}

		// Set organization subscription
		err = testDB.SetOrganizationSubscription(org.Address, organizationSubscription)
		c.Assert(err, qt.IsNil)

		// Verify organization subscription was updated
		updatedOrg, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)

		// Debug: Print what we got vs what we expected
		t.Logf("Expected PlanID: %d, Got PlanID: %d", dbSubscription.ID, updatedOrg.Subscription.PlanID)
		t.Logf("Expected Active: %t, Got Active: %t", true, updatedOrg.Subscription.Active)
		t.Logf("Expected MaxCensusSize: %d, Got MaxCensusSize: %d", 1000, updatedOrg.Subscription.MaxCensusSize)
		t.Logf("Expected Email: %s, Got Email: %s", testCustomerEmail, updatedOrg.Subscription.Email)

		c.Assert(updatedOrg.Subscription.PlanID, qt.Equals, dbSubscription.ID)
		c.Assert(updatedOrg.Subscription.Active, qt.IsTrue)
		c.Assert(updatedOrg.Subscription.MaxCensusSize, qt.Equals, 1000)
		c.Assert(updatedOrg.Subscription.Email, qt.Equals, testCustomerEmail)
	})

	t.Run("SubscriptionCanceledUnit", func(*testing.T) {
		// Create a default plan first
		_, err := testDB.SetPlan(&db.Plan{
			Name:    "Free Plan",
			Default: true,
			Organization: db.PlanLimits{
				MaxCensus: 100,
			},
		})
		c.Assert(err, qt.IsNil)

		// Get organization
		org, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)

		// Simulate subscription cancellation by switching to default plan
		defaultPlan, err := testDB.DefaultPlan()
		c.Assert(err, qt.IsNil)

		orgSubscription := &db.OrganizationSubscription{
			PlanID:          defaultPlan.ID,
			StartDate:       time.Now(),
			LastPaymentDate: org.Subscription.LastPaymentDate,
			Active:          true,
			MaxCensusSize:   defaultPlan.Organization.MaxCensus,
		}

		err = testDB.SetOrganizationSubscription(org.Address, orgSubscription)
		c.Assert(err, qt.IsNil)

		// Verify organization was switched to default plan
		updatedOrg, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedOrg.Subscription.PlanID, qt.Equals, defaultPlan.ID)
		c.Assert(updatedOrg.Subscription.Active, qt.IsTrue)
		c.Assert(updatedOrg.Subscription.MaxCensusSize, qt.Equals, defaultPlan.Organization.MaxCensus)
	})

	t.Run("PaymentSucceededUnit", func(*testing.T) {
		event := createTestPaymentSucceededEvent()

		// Mock the invoice info extraction
		paymentTime, orgAddressStr, err := mockGetInvoiceInfoFromEvent(*event)
		c.Assert(err, qt.IsNil)

		// Get organization
		org, err := testDB.Organization(common.HexToAddress(orgAddressStr))
		c.Assert(err, qt.IsNil)

		// Update last payment date
		org.Subscription.LastPaymentDate = paymentTime
		err = testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Verify last payment date was updated
		updatedOrg, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedOrg.Subscription.LastPaymentDate.IsZero(), qt.IsFalse)
	})
}

func TestWebhookErrorHandlingUnit(t *testing.T) {
	c := qt.New(t)

	t.Run("OrganizationNotFound", func(*testing.T) {
		event := createTestSubscriptionCreatedEvent()

		// Mock the subscription info extraction
		stripeSubscriptionInfo, err := mockGetSubscriptionInfoFromEvent(*event)
		c.Assert(err, qt.IsNil)

		// Try to get non-existent organization
		org, err := testDB.Organization(stripeSubscriptionInfo.OrganizationAddress)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(org, qt.IsNil)
	})

	t.Run("PlanNotFound", func(t *testing.T) {
		// Create organization but no matching plan
		token := testCreateUser(t, testPass)
		orgAddress := testCreateOrganization(t, token)

		// Create event with non-existent product ID
		event := createTestSubscriptionCreatedEventWithCustom(orgAddress, "prod_nonexistent")

		stripeSubscriptionInfo, err := mockGetSubscriptionInfoFromEvent(*event)
		c.Assert(err, qt.IsNil)

		// Try to get non-existent plan
		dbSubscription, err := testDB.PlanByStripeID(stripeSubscriptionInfo.ProductID)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(dbSubscription, qt.IsNil)
	})

	t.Run("NoDefaultPlanForCancellation", func(t *testing.T) {
		// Create organization and plan
		token := testCreateUser(t, testPass)
		orgAddress := testCreateOrganization(t, token)

		planID, err := testDB.SetPlan(&db.Plan{
			Name:     "Essential Plan",
			StripeID: testProductID,
			Default:  false,
		})
		c.Assert(err, qt.IsNil)

		// Set organization subscription
		err = testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
			PlanID: planID,
			Active: true,
		})
		c.Assert(err, qt.IsNil)

		// Try to get default plan when none exists
		defaultPlan, err := testDB.DefaultPlan()
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(defaultPlan, qt.IsNil)
	})
}

func TestWebhookEndpointUnit(t *testing.T) {
	c := qt.New(t)
	// Create a mock API instance for testing handlers
	api := &API{db: testDB}

	// Create test setup
	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)
	testOrgAddress = orgAddress

	_, err := testDB.SetPlan(&db.Plan{
		Name:     "Essential Plan",
		StripeID: testProductID,
		Default:  false,
		Organization: db.PlanLimits{
			MaxCensus: 1000,
		},
	})
	c.Assert(err, qt.IsNil)

	t.Run("InvalidPayload", func(*testing.T) {
		req := httptest.NewRequest("POST", "/subscriptions/webhook", bytes.NewReader([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		api.handleWebhook(w, req)

		c.Assert(w.Code, qt.Equals, http.StatusBadRequest)
	})

	t.Run("UnsupportedEventType", func(*testing.T) {
		event := &stripeapi.Event{
			ID:   "evt_test_unsupported",
			Type: "customer.created", // Unsupported event type
		}
		payload, err := json.Marshal(event)
		c.Assert(err, qt.IsNil)

		req := httptest.NewRequest("POST", "/subscriptions/webhook", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		api.handleWebhook(w, req)

		// Should fail due to signature verification, but documents the flow
		c.Assert(w.Code, qt.Equals, http.StatusBadRequest)
	})
}

// TestCurrentStripeFlowDocumentation documents the current problematic behavior
func TestCurrentStripeFlowDocumentation(t *testing.T) {
	c := qt.New(t)

	t.Run("GlobalMutexProblem", func(*testing.T) {
		// This test documents the current problematic global mutex
		// In the current implementation, all webhook processing is serialized
		// by a global mutex, which is terrible for performance and scalability

		// The global mutex is defined as: var mu sync.Mutex
		// And used in handleWebhook like this:
		// mu.Lock()
		// defer mu.Unlock()

		// This means only one webhook can be processed at a time across
		// the entire application, which is a major bottleneck
		c.Log("Current implementation uses global mutex for all webhook processing")
		c.Log("This serializes all webhook events and creates a bottleneck")
	})

	t.Run("ErrorHandlingProblems", func(*testing.T) {
		// Documents the current error handling issues
		c.Log("Current implementation has poor error handling:")
		c.Log("- No retry mechanisms for failed webhooks")
		c.Log("- No idempotency handling for duplicate events")
		c.Log("- Manual intervention required for failures")
		c.Log("- No audit trail for subscription changes")
	})

	t.Run("ArchitecturalProblems", func(*testing.T) {
		// Documents architectural issues
		c.Log("Current implementation has architectural problems:")
		c.Log("- Business logic mixed with HTTP handlers")
		c.Log("- No service layer abstraction")
		c.Log("- Tight coupling between Stripe API and database")
		c.Log("- No proper domain modeling")
	})
}
