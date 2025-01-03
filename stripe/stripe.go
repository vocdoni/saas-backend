package stripe

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stripe/stripe-go/v81"
	portalSession "github.com/stripe/stripe-go/v81/billingportal/session"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/customer"
	"github.com/stripe/stripe-go/v81/price"
	"github.com/stripe/stripe-go/v81/product"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// ProductsIDs contains the Stripe product IDs for different subscription tiers
var ProductsIDs = []string{
	"prod_R3LTVsjklmuQAL", // Essential
	"prod_R0kTryoMNl8I19", // Premium
	"prod_RFObcbvED7MYbz", // Free
	"prod_RHurAb3OjkgJRy", // Custom
}

// ReturnStatus represents the response structure for checkout session status
type ReturnStatus struct {
	Status             string `json:"status"`
	CustomerEmail      string `json:"customer_email"`
	SubscriptionStatus string `json:"subscription_status"`
}

// StripeSubscriptionInfo represents the information related to a Stripe subscription
// that are relevant for the application.
type StripeSubscriptionInfo struct {
	ID                  string
	Status              string
	ProductID           string
	Quantity            int
	OrganizationAddress string
	CustomerEmail       string
	StartDate           time.Time
	EndDate             time.Time
}

// StripeClient is a client for interacting with the Stripe API.
// It holds the necessary configuration such as the webhook secret.
type StripeClient struct {
	webhookSecret string
}

// New creates a new instance of StripeClient with the provided API secret and webhook secret.
// It sets the Stripe API key to the provided apiSecret.
func New(apiSecret, webhookSecret string) *StripeClient {
	stripe.Key = apiSecret
	return &StripeClient{
		webhookSecret: webhookSecret,
	}
}

// DecodeEvent decodes a Stripe webhook event from the given payload and signature header.
// It verifies the webhook signature and returns the decoded event or an error if validation fails.
func (s *StripeClient) DecodeEvent(payload []byte, signatureHeader string) (*stripe.Event, error) {
	event := stripe.Event{}
	if err := json.Unmarshal(payload, &event); err != nil {
		log.Errorf("stripe webhook: error while parsing basic request. %s\n", err.Error())
		return nil, err
	}

	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		log.Errorf("stripe webhook: webhook signature verification failed. %s\n", err.Error())
		return nil, err
	}
	return &event, nil
}

func (s *StripeClient) GetInvoiceInfoFromEvent(event stripe.Event) (time.Time, string, error) {
	var invoice stripe.Invoice
	err := json.Unmarshal(event.Data.Raw, &invoice)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("error parsing webhook JSON: %v\n", err)
	}
	if invoice.EffectiveAt == 0 {
		return time.Time{}, "", fmt.Errorf("invoice %s does not contain an effective date", invoice.ID)
	}
	if invoice.SubscriptionDetails == nil {
		return time.Time{}, "", fmt.Errorf("invoice %s does not contain subscription details", invoice.ID)
	}
	return time.Unix(invoice.EffectiveAt, 0), invoice.SubscriptionDetails.Metadata["address"], nil
}

// GetSubscriptionInfoFromEvent processes a Stripe event to extract subscription information.
// It unmarshals the event data and retrieves the associated customer and subscription details.
func (s *StripeClient) GetSubscriptionInfoFromEvent(event stripe.Event) (*StripeSubscriptionInfo, error) {
	var subscription stripe.Subscription
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		return &StripeSubscriptionInfo{}, fmt.Errorf("error parsing webhook JSON: %v\n", err)
	}

	params := &stripe.CustomerParams{}
	customer, err := customer.Get(subscription.Customer.ID, params)
	if err != nil || customer == nil {
		return &StripeSubscriptionInfo{}, fmt.Errorf(
			"could not update subscription %s, stripe internal error getting customer",
			subscription.ID,
		)
	}
	address := subscription.Metadata["address"]
	if len(address) == 0 {
		return &StripeSubscriptionInfo{}, fmt.Errorf("subscription %s does not contain an address in metadata", subscription.ID)
	}

	if len(subscription.Items.Data) == 0 {
		return &StripeSubscriptionInfo{}, fmt.Errorf("subscription %s does not contain any items", subscription.ID)
	}

	return &StripeSubscriptionInfo{
		ID:                  subscription.ID,
		Status:              string(subscription.Status),
		ProductID:           subscription.Items.Data[0].Plan.Product.ID,
		Quantity:            int(subscription.Items.Data[0].Quantity),
		OrganizationAddress: address,
		CustomerEmail:       customer.Email,
		StartDate:           time.Unix(subscription.CurrentPeriodStart, 0),
		EndDate:             time.Unix(subscription.CurrentPeriodEnd, 0),
	}, nil
}

// GetPriceByID retrieves a Stripe price object by its ID.
// It searches for an active price with the given lookup key.
// Returns nil if no matching price is found.
func (s *StripeClient) GetPriceByID(priceID string) *stripe.Price {
	params := &stripe.PriceSearchParams{
		SearchParams: stripe.SearchParams{
			Query: fmt.Sprintf("active:'true' AND lookup_key:'%s'", priceID),
		},
	}
	params.AddExpand("data.tiers")
	if results := price.Search(params); results.Next() {
		return results.Price()
	}
	return nil
}

// GetProductByID retrieves a Stripe product by its ID.
// It expands the default price and its tiers in the response.
// Returns the product object and any error encountered.
func (s *StripeClient) GetProductByID(productID string) (*stripe.Product, error) {
	params := &stripe.ProductParams{}
	params.AddExpand("default_price")
	params.AddExpand("default_price.tiers")
	product, err := product.Get(productID, params)
	if err != nil {
		return nil, err
	}
	return product, nil
}

// GetPrices retrieves multiple Stripe prices by their IDs.
// It returns a slice of Price objects for all valid price IDs.
// Invalid or non-existent price IDs are silently skipped.
func (s *StripeClient) GetPrices(priceIDs []string) []*stripe.Price {
	var prices []*stripe.Price
	for _, priceID := range priceIDs {
		if price := s.GetPriceByID(priceID); price != nil {
			prices = append(prices, price)
		}
	}
	return prices
}

// GetPlans retrieves and constructs a list of subscription plans from Stripe products.
// It processes product metadata to extract organization limits, voting types, and features.
// Returns a slice of Plan objects and any error encountered during processing.
func (s *StripeClient) GetPlans() ([]*db.Plan, error) {
	var plans []*db.Plan
	for i, productID := range ProductsIDs {
		if product, err := s.GetProductByID(productID); product != nil && err == nil {
			var organizationData db.PlanLimits
			if err := json.Unmarshal([]byte(product.Metadata["organization"]), &organizationData); err != nil {
				return nil, fmt.Errorf("error parsing plan organization metadata JSON: %s\n", err.Error())
			}
			var votingTypesData db.VotingTypes
			if err := json.Unmarshal([]byte(product.Metadata["votingTypes"]), &votingTypesData); err != nil {
				return nil, fmt.Errorf("error parsing plan voting types metadata JSON: %s\n", err.Error())
			}
			var featuresData db.Features
			if err := json.Unmarshal([]byte(product.Metadata["features"]), &featuresData); err != nil {
				return nil, fmt.Errorf("error parsing plan features metadata JSON: %s\n", err.Error())
			}
			price := product.DefaultPrice
			startingPrice := price.UnitAmount
			if len(price.Tiers) > 0 {
				startingPrice = price.Tiers[0].FlatAmount
			}
			var tiers []db.PlanTier
			for _, tier := range price.Tiers {
				if tier.UpTo == 0 {
					continue
				}
				tiers = append(tiers, db.PlanTier{
					Amount: tier.FlatAmount,
					UpTo:   tier.UpTo,
				})
			}
			plans = append(plans, &db.Plan{
				ID:              uint64(i),
				Name:            product.Name,
				StartingPrice:   startingPrice,
				StripeID:        productID,
				StripePriceID:   price.ID,
				Default:         price.Metadata["Default"] == "true",
				Organization:    organizationData,
				VotingTypes:     votingTypesData,
				Features:        featuresData,
				CensusSizeTiers: tiers,
			})
		} else {
			return nil, fmt.Errorf("error getting product %s: %s", productID, err.Error())
		}
	}
	return plans, nil
}

// CreateSubscriptionCheckoutSession creates a new Stripe checkout session for a subscription.
// It configures the session with the specified price, amount return URL, and subscription metadata.
// The email provided is used in order to uniquely distinguish the customer on the Stripe side.
// The priceID is that is provided corrsponds to the subscription tier selected by the user.
// Returns the created checkout session and any error encountered.
// Overview of stripe checkout mechanics: https://docs.stripe.com/checkout/custom/quickstart
// API description https://docs.stripe.com/api/checkout/sessions
func (s *StripeClient) CreateSubscriptionCheckoutSession(
	priceID, returnURL, address, email, locale string, amount int64,
) (*stripe.CheckoutSession, error) {
	if len(locale) == 0 {
		locale = "auto"
	}
	if locale == "ca" {
		locale = "es"
	}
	checkoutParams := &stripe.CheckoutSessionParams{
		// Subscription mode
		Mode:          stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		CustomerEmail: &email,
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(amount),
			},
		},
		// UI mode is set to embedded, since the client is integrated in our UI
		UIMode: stripe.String(string(stripe.CheckoutSessionUIModeEmbedded)),
		// Automatic tax calculation is enabled
		AutomaticTax: &stripe.CheckoutSessionAutomaticTaxParams{
			Enabled: stripe.Bool(true),
		},
		// We store in the metadata the address of the organization
		SubscriptionData: &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"address": address,
			},
		},
		// The locale is being used to configure the language of the embedded client
		Locale: stripe.String(locale),
	}

	// The returnURL is used to redirect the user after the payment is completed
	if len(returnURL) > 0 {
		checkoutParams.ReturnURL = stripe.String(returnURL + "/{CHECKOUT_SESSION_ID}")
	} else {
		checkoutParams.RedirectOnCompletion = stripe.String("never")
	}
	session, err := session.New(checkoutParams)
	if err != nil {
		return nil, err
	}

	return session, nil
}

// RetrieveCheckoutSession retrieves a checkout session from Stripe by session ID.
// It returns a ReturnStatus object and an error if any.
// The ReturnStatus object contains information about the session status,
// customer email, and subscription status.
func (s *StripeClient) RetrieveCheckoutSession(sessionID string) (*ReturnStatus, error) {
	params := &stripe.CheckoutSessionParams{}
	params.AddExpand("line_items")
	sess, err := session.Get(sessionID, params)
	if err != nil {
		return nil, err
	}
	data := &ReturnStatus{
		Status:             string(sess.Status),
		CustomerEmail:      sess.CustomerDetails.Email,
		SubscriptionStatus: string(sess.Subscription.Status),
	}
	return data, nil
}

// CreatePortalSession creates a new billing portal session for a customer based on an email address.
func (s *StripeClient) CreatePortalSession(customerEmail string) (*stripe.BillingPortalSession, error) {
	// get stripe customer based on provided email
	customerParams := &stripe.CustomerListParams{
		Email: stripe.String(customerEmail),
	}
	var customerID string
	if customers := customer.List(customerParams); customers.Next() {
		customerID = customers.Customer().ID
	} else {
		return nil, fmt.Errorf("could not find customer with email %s", customerEmail)
	}

	params := &stripe.BillingPortalSessionParams{
		Customer: &customerID,
	}
	return portalSession.New(params)
}
