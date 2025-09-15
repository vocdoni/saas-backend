package stripe

import (
	"fmt"

	stripeapi "github.com/stripe/stripe-go/v81"
	stripeportalsession "github.com/stripe/stripe-go/v81/billingportal/session"
	stripecheckoutsession "github.com/stripe/stripe-go/v81/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v81/customer"
	stripeprice "github.com/stripe/stripe-go/v81/price"
	stripeproduct "github.com/stripe/stripe-go/v81/product"
	stripewebhook "github.com/stripe/stripe-go/v81/webhook"
	"github.com/vocdoni/saas-backend/errors"
)

// Client wraps the Stripe API client with additional functionality
type Client struct {
	config *Config
}

// NewClient creates a new Stripe client with the given configuration
func NewClient(config *Config) *Client {
	stripeapi.Key = config.APIKey

	return &Client{
		config: config,
	}
}

// ValidateWebhookEvent validates and parses a webhook event
func (c *Client) ValidateWebhookEvent(payload []byte, signatureHeader string) (*stripeapi.Event, error) {
	event, err := stripewebhook.ConstructEvent(payload, signatureHeader, c.config.WebhookSecret)
	if err != nil {
		return nil, errors.ErrMalformedBody.Withf("webhook signature validation failed: %v", err)
	}
	return &event, nil
}

// GetCustomer retrieves a customer by ID
func (*Client) GetCustomer(customerID string) (*stripeapi.Customer, error) {
	params := &stripeapi.CustomerParams{}
	customer, err := stripecustomer.Get(customerID, params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get customer: %v", err)
	}
	return customer, nil
}

// GetCustomerByEmail retrieves a customer by email address
func (*Client) GetCustomerByEmail(email string) (*stripeapi.Customer, error) {
	params := &stripeapi.CustomerListParams{
		Email: stripeapi.String(email),
	}

	customers := stripecustomer.List(params)
	if !customers.Next() {
		return nil, errors.ErrUserNotFound.Withf("customer with email %s not found", email)
	}

	return customers.Customer(), nil
}

// GetProduct retrieves a product by ID with expanded default price
func (*Client) GetProduct(productID string) (*stripeapi.Product, error) {
	params := &stripeapi.ProductParams{}
	params.AddExpand("default_price")

	product, err := stripeproduct.Get(productID, params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get product: %v", err)
	}
	return product, nil
}

// GetPrice retrieves a price by lookup key
func (*Client) GetPrice(lookupKey string) (*stripeapi.Price, error) {
	params := &stripeapi.PriceSearchParams{
		SearchParams: stripeapi.SearchParams{
			Query: fmt.Sprintf("active:'true' AND lookup_key:'%s'", lookupKey),
		},
	}

	results := stripeprice.Search(params)
	if !results.Next() {
		return nil, errors.ErrPlanNotFound.Withf("price with lookup key %s not found", lookupKey)
	}

	return results.Price(), nil
}

// CreateCheckoutSession creates a new checkout session for subscription
// It configures the session with the specified price, amount return URL, and subscription metadata.
// The email provided is used in order to uniquely distinguish the customer on the Stripe side.
// The priceID is that is provided corresponds to the subscription tier selected by the user.
// Returns the created checkout session and any error encountered.
// Overview of stripe checkout mechanics: https://docs.stripe.com/checkout/custom/quickstart
// API description https://docs.stripe.com/api/checkout/sessions
func (*Client) CreateCheckoutSession(params *CheckoutSessionParams) (*stripeapi.CheckoutSession, error) {
	if params.Locale == "" {
		params.Locale = "auto"
	}
	if params.Locale == "ca" {
		params.Locale = "es"
	}

	checkoutParams := &stripeapi.CheckoutSessionParams{
		// Subscription mode
		Mode:          stripeapi.String(string(stripeapi.CheckoutSessionModeSubscription)),
		CustomerEmail: &params.CustomerEmail,
		LineItems: []*stripeapi.CheckoutSessionLineItemParams{
			{
				Price:    stripeapi.String(params.PriceID),
				Quantity: stripeapi.Int64(params.Quantity),
			},
		},
		// UI mode is set to embedded, since the client is integrated in our UI
		UIMode: stripeapi.String(string(stripeapi.CheckoutSessionUIModeEmbedded)),
		// Automatic tax calculation is enabled
		AutomaticTax: &stripeapi.CheckoutSessionAutomaticTaxParams{
			Enabled: stripeapi.Bool(true),
		},
		// We store in the metadata the address of the organization
		SubscriptionData: &stripeapi.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"address": params.OrgAddress,
			},
		},
		// The locale is being used to configure the language of the embedded client
		Locale: stripeapi.String(params.Locale),
	}

	// The returnURL is used to redirect the user after the payment is completed
	if params.ReturnURL != "" {
		checkoutParams.ReturnURL = stripeapi.String(params.ReturnURL + "/{CHECKOUT_SESSION_ID}")
	} else {
		checkoutParams.RedirectOnCompletion = stripeapi.String("never")
	}

	session, err := stripecheckoutsession.New(checkoutParams)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to create checkout session: %v", err)
	}

	return session, nil
}

// GetCheckoutSession retrieves a checkout session by ID
func (*Client) GetCheckoutSession(sessionID string) (*CheckoutSessionStatus, error) {
	params := &stripeapi.CheckoutSessionParams{}
	params.AddExpand("line_items")

	session, err := stripecheckoutsession.Get(sessionID, params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get checkout session: %v", err)
	}

	status := &CheckoutSessionStatus{
		Status:             string(session.Status),
		CustomerEmail:      session.CustomerDetails.Email,
		SubscriptionStatus: string(session.Subscription.Status),
	}

	return status, nil
}

// CreatePortalSession creates a billing portal session for a customer
func (c *Client) CreatePortalSession(customerEmail string) (*stripeapi.BillingPortalSession, error) {
	customer, err := c.GetCustomerByEmail(customerEmail)
	if err != nil {
		return nil, err
	}

	params := &stripeapi.BillingPortalSessionParams{
		Customer: &customer.ID,
	}

	session, err := stripeportalsession.New(params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to create portal session: %v", err)
	}

	return session, nil
}

// CheckoutSessionParams holds parameters for creating a checkout session
type CheckoutSessionParams struct {
	PriceID       string
	ReturnURL     string
	OrgAddress    string
	CustomerEmail string
	Locale        string
	Quantity      int64
}

// CheckoutSessionStatus represents the status of a checkout session
type CheckoutSessionStatus struct {
	Status             string `json:"status"`
	CustomerEmail      string `json:"customer_email"`
	SubscriptionStatus string `json:"subscription_status"`
}
