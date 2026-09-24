package stripe

import (
	"fmt"

	stripeapi "github.com/stripe/stripe-go/v86"
	stripeportalsession "github.com/stripe/stripe-go/v86/billingportal/session"
	stripecheckoutsession "github.com/stripe/stripe-go/v86/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v86/customer"
	stripeprice "github.com/stripe/stripe-go/v86/price"
	stripeproduct "github.com/stripe/stripe-go/v86/product"
	stripewebhook "github.com/stripe/stripe-go/v86/webhook"
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
		return nil, fmt.Errorf("webhook signature validation failed: %v", err)
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
		Email: new(email),
	}

	customers := stripecustomer.List(params)
	if !customers.Next() {
		return nil, errors.ErrStripeError.Withf("customer with email %s not found", email)
	}

	return customers.Customer(), nil
}

// UpdateCustomerMetadata updates a customer's metadata
func (*Client) UpdateCustomerMetadata(customerID string, metadata map[string]string) error {
	params := &stripeapi.CustomerParams{
		Metadata: metadata,
	}
	_, err := stripecustomer.Update(customerID, params)
	if err != nil {
		return errors.ErrStripeError.Withf("failed to update customer metadata: %v", err)
	}
	return nil
}

// GetCustomerByAddress retrieves a customer by the organization EVM address
func (*Client) GetCustomerByAddress(address string) (*stripeapi.Customer, error) {
	customers := stripecustomer.Search(&stripeapi.CustomerSearchParams{
		SearchParams: stripeapi.SearchParams{
			Query: fmt.Sprintf("metadata['address']:'%s'", address),
			Limit: new(int64(1)),
		},
	})

	if !customers.Next() {
		return nil, errors.ErrStripeError.Withf("customer with address %s not found", address)
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

// ListProducts retrieves all active products, with each product's default price expanded so
// callers can inspect default-price metadata (e.g. the default-plan marker).
func (*Client) ListProducts() ([]stripeapi.Product, error) {
	var products []stripeapi.Product

	params := &stripeapi.ProductListParams{
		Active: new(true),
	}
	params.AddExpand("data.default_price")
	params.Filters.AddFilter("limit", "", "100")

	i := stripeproduct.List(params)
	for i.Next() {
		products = append(products, *i.Product())
	}
	if err := i.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to list products: %v", err)
	}

	return products, nil
}

// GetProductPrices retrieves all active prices for a given product ID
func (*Client) GetProductPrices(productID string) ([]stripeapi.Price, error) {
	var prices []stripeapi.Price

	params := &stripeapi.PriceListParams{
		Product: new(productID),
		Active:  new(true),
	}
	params.Filters.AddFilter("limit", "", "100") // Adjust limit as needed

	i := stripeprice.List(params)
	for i.Next() {
		prices = append(prices, *i.Price())
	}
	if err := i.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to list prices: %v", err)
	}

	return prices, nil
}

// stripeLocale maps an organization language onto the locale of the embedded checkout
// client: an empty language lets Stripe pick from the browser, and Catalan is served in
// Spanish because Stripe has no "ca" locale.
// TODO(#694): fold this into the single localization source of truth.
func stripeLocale(lang string) string {
	switch lang {
	case "":
		return "auto"
	case "ca":
		return "es"
	default:
		return lang
	}
}

// CreateCheckoutSession creates a new checkout session for subscription
// It configures the session with the specified price, amount return URL, and subscription metadata.
// The email provided is used in order to uniquely distinguish the customer on the Stripe side.
// The priceID is that is provided corresponds to the subscription tier selected by the user.
// Returns the created checkout session and any error encountered.
// Overview of stripe checkout mechanics: https://docs.stripe.com/checkout/custom/quickstart
// API description https://docs.stripe.com/api/checkout/sessions
func (c *Client) CreateCheckoutSession(params *CheckoutSessionParams) (*stripeapi.CheckoutSession, error) {
	checkoutParams := &stripeapi.CheckoutSessionParams{
		// Subscription mode
		Mode: new(string(stripeapi.CheckoutSessionModeSubscription)),
		LineItems: []*stripeapi.CheckoutSessionLineItemParams{
			{
				Price:    new(params.PriceID),
				Quantity: new(params.Quantity),
			},
		},
		// UI mode is set to embedded, since the client is integrated in our UI
		UIMode: new(string(stripeapi.CheckoutSessionUIModeElements)),
		// Automatic tax calculation is enabled
		AutomaticTax: &stripeapi.CheckoutSessionAutomaticTaxParams{
			Enabled: new(true),
		},
		// We store in the metadata the address of the organization
		SubscriptionData: &stripeapi.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"address": params.OrgAddress,
			},
		},
		TaxIDCollection: &stripeapi.CheckoutSessionTaxIDCollectionParams{
			Enabled: new(true),
		},
		AllowPromotionCodes:      new(true),
		BillingAddressCollection: new(string(stripeapi.CheckoutSessionBillingAddressCollectionAuto)),
		// The locale is being used to configure the language of the embedded client
		Locale: new(stripeLocale(params.Locale)),
	}

	if params.FreeTrialDays > 0 {
		checkoutParams.SubscriptionData.TrialPeriodDays = new(int64(params.FreeTrialDays))
	}

	customer, err := c.GetCustomerByAddress(params.OrgAddress)
	if err != nil {
		checkoutParams.CustomerEmail = new(params.CustomerEmail)
	} else {
		checkoutParams.Customer = &customer.ID
		checkoutParams.CustomerUpdate = &stripeapi.CheckoutSessionCustomerUpdateParams{
			Name:    new("auto"),
			Address: new("auto"),
		}
	}

	// The returnURL is used to redirect the user after the payment is completed
	if params.ReturnURL != "" {
		checkoutParams.ReturnURL = new(params.ReturnURL + "/{CHECKOUT_SESSION_ID}")
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

	return checkoutSessionStatus(session), nil
}

// checkoutSessionStatus maps a Stripe checkout session onto the API status shape.
// Both sub-objects are optional: CustomerDetails is nil until the customer fills the
// form, and Subscription is nil for one-time (mode "payment") sessions.
func checkoutSessionStatus(session *stripeapi.CheckoutSession) *CheckoutSessionStatus {
	status := &CheckoutSessionStatus{
		Status:        string(session.Status),
		PaymentStatus: string(session.PaymentStatus),
	}
	if session.CustomerDetails != nil {
		status.CustomerEmail = session.CustomerDetails.Email
	}
	if session.Subscription != nil {
		status.SubscriptionStatus = string(session.Subscription.Status)
	}
	return status
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
	FreeTrialDays int
}

// CheckoutSessionStatus represents the status of a checkout session
type CheckoutSessionStatus struct {
	Status             string `json:"status"`
	CustomerEmail      string `json:"customer_email"`
	SubscriptionStatus string `json:"subscription_status"`
	// PaymentStatus is the payment outcome of a one-time (mode "payment") session:
	// "paid", "unpaid" or "no_payment_required"; empty for subscription sessions.
	PaymentStatus string `json:"payment_status,omitempty"`
}
