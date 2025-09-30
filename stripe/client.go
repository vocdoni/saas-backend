package stripe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v82"
	stripeportalsession "github.com/stripe/stripe-go/v82/billingportal/session"
	stripecheckoutsession "github.com/stripe/stripe-go/v82/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v82/customer"
	stripeprice "github.com/stripe/stripe-go/v82/price"
	stripeproduct "github.com/stripe/stripe-go/v82/product"
	stripewebhook "github.com/stripe/stripe-go/v82/webhook"
)

// Client wraps the Stripe API client with additional functionality
type Client struct {
	config     *Config
	httpClient *http.Client
}

// NewClient creates a new Stripe client with the given configuration
func NewClient(config *Config) *Client {
	stripeapi.Key = config.APIKey

	return &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ValidateWebhookEvent validates and parses a webhook event
func (c *Client) ValidateWebhookEvent(payload []byte, signatureHeader string) (*stripeapi.Event, error) {
	event, err := stripewebhook.ConstructEvent(payload, signatureHeader, c.config.WebhookSecret)
	if err != nil {
		return nil, NewStripeError("webhook_validation", "webhook signature validation failed", err)
	}
	return &event, nil
}

// GetCustomer retrieves a customer by ID
func (*Client) GetCustomer(customerID string) (*stripeapi.Customer, error) {
	params := &stripeapi.CustomerParams{}
	customer, err := stripecustomer.Get(customerID, params)
	if err != nil {
		return nil, NewStripeError("api_call_failed", "failed to get customer", err)
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
		return nil, NewStripeError("customer_not_found", fmt.Sprintf("customer with email %s not found", email), nil)
	}

	return customers.Customer(), nil
}

// GetProduct retrieves a product by ID with expanded default price
func (*Client) GetProduct(productID string) (*stripeapi.Product, error) {
	params := &stripeapi.ProductParams{}
	params.AddExpand("default_price")

	product, err := stripeproduct.Get(productID, params)
	if err != nil {
		return nil, NewStripeError("api_call_failed", "failed to get product", err)
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
		return nil, NewStripeError("price_not_found", fmt.Sprintf("price with lookup key %s not found", lookupKey), nil)
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
		return nil, NewStripeError("api_call_failed", "failed to create checkout session", err)
	}

	return session, nil
}

// GetCheckoutSession retrieves a checkout session by ID
func (*Client) GetCheckoutSession(sessionID string) (*CheckoutSessionStatus, error) {
	params := &stripeapi.CheckoutSessionParams{}
	params.AddExpand("line_items")

	session, err := stripecheckoutsession.Get(sessionID, params)
	if err != nil {
		return nil, NewStripeError("api_call_failed", "failed to get checkout session", err)
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
		return nil, NewStripeError("api_call_failed", "failed to create portal session", err)
	}

	return session, nil
}

// ParseSubscriptionFromEvent extracts subscription information from a webhook event
func (c *Client) ParseSubscriptionFromEvent(event *stripeapi.Event) (*SubscriptionInfo, error) {
	var subscription stripeapi.Subscription
	if err := json.Unmarshal(event.Data.Raw, &subscription); err != nil {
		return nil, NewStripeError("invalid_event", "failed to parse subscription from event", err)
	}

	customer, err := c.GetCustomer(subscription.Customer.ID)
	if err != nil {
		return nil, err
	}

	orgAddress := common.HexToAddress(subscription.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, NewStripeError("invalid_event", "subscription missing address metadata", nil)
	}

	if len(subscription.Items.Data) == 0 {
		return nil, NewStripeError("invalid_event", "subscription has no items", nil)
	}

	return &SubscriptionInfo{
		ID:            subscription.ID,
		Status:        subscription.Status,
		ProductID:     subscription.Items.Data[0].Plan.Product.ID,
		OrgAddress:    orgAddress,
		CustomerEmail: customer.Email,
		StartDate:     time.Unix(subscription.Items.Data[0].CurrentPeriodStart, 0),
		EndDate:       time.Unix(subscription.Items.Data[0].CurrentPeriodEnd, 0),
	}, nil
}

// ParseInvoiceFromEvent extracts invoice information from a webhook event
func (*Client) ParseInvoiceFromEvent(event *stripeapi.Event) (*InvoiceInfo, error) {
	var invoice stripeapi.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return nil, NewStripeError("invalid_event", "failed to parse invoice from event", err)
	}

	if invoice.EffectiveAt == 0 {
		return nil, NewStripeError("invalid_event", "invoice missing effective date", nil)
	}

	if invoice.Parent.SubscriptionDetails == nil || invoice.Parent.Type != "subscription_details" {
		return nil, NewStripeError("invalid_event", "invoice missing subscription details", nil)
	}
	orgAddress := common.HexToAddress(invoice.Parent.SubscriptionDetails.Metadata["address"])
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, NewStripeError("invalid_event", "invoice missing address metadata", nil)
	}

	return &InvoiceInfo{
		ID:          invoice.ID,
		PaymentTime: time.Unix(invoice.EffectiveAt, 0),
		OrgAddress:  orgAddress,
	}, nil
}

// ParseProductFromEvent extracts product information from a webhook event
func (*Client) ParseProductFromEvent(event *stripeapi.Event) (*stripeapi.Product, error) {
	var product stripeapi.Product
	if err := json.Unmarshal(event.Data.Raw, &product); err != nil {
		return nil, NewStripeError("invalid_event", "failed to parse product from event", err)
	}

	return &product, nil
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

// SubscriptionInfo represents the information related to a Stripe subscription
// that are relevant for the application.
type SubscriptionInfo struct {
	ID            string
	Status        stripeapi.SubscriptionStatus
	ProductID     string
	OrgAddress    common.Address
	CustomerEmail string
	StartDate     time.Time
	EndDate       time.Time
}

// InvoiceInfo represents invoice information extracted from events
type InvoiceInfo struct {
	ID          string
	PaymentTime time.Time
	OrgAddress  common.Address
}
