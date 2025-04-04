// Package stripe provides integration with the Stripe payment service,
// handling subscriptions, invoices, and webhook events.
package stripe

import (
	"encoding/json"
	"fmt"
	"time"

	//revive:disable:import-alias-naming
	stripeapi "github.com/stripe/stripe-go/v81"
	stripePortalSession "github.com/stripe/stripe-go/v81/billingportal/session"
	stripeCheckoutSession "github.com/stripe/stripe-go/v81/checkout/session"
	stripeCustomer "github.com/stripe/stripe-go/v81/customer"
	stripePrice "github.com/stripe/stripe-go/v81/price"
	stripeProduct "github.com/stripe/stripe-go/v81/product"
	stripeWebhook "github.com/stripe/stripe-go/v81/webhook"
	stripeDB "github.com/vocdoni/saas-backend/db"
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

// SubscriptionInfo represents the information related to a Stripe subscription
// that are relevant for the application.
type SubscriptionInfo struct {
	ID                  string
	Status              string
	ProductID           string
	Quantity            int
	OrganizationAddress string
	CustomerEmail       string
	StartDate           time.Time
	EndDate             time.Time
}

// whSecret is the global webhook secret.
var (
	whSecret    string
	initialized bool
)

// Init initializes the global Stripe client configuration.
// It must be called once during application startup.
func Init(apiSecret, webhookSecret string) {
	if initialized {
		panic("stripe.Init called more than once")
	}
	stripeapi.Key = apiSecret
	whSecret = webhookSecret
	initialized = true
}

// DecodeEvent decodes a Stripe webhook event from the given payload and signature header.
// It verifies the webhook signature and returns the decoded event or an error if validation fails.
func DecodeEvent(payload []byte, signatureHeader string) (*stripeapi.Event, error) {
	event := stripeapi.Event{}
	if err := json.Unmarshal(payload, &event); err != nil {
		log.Errorf("stripe webhook: error while parsing basic request. %s", err.Error())
		return nil, err
	}

	event, err := stripeWebhook.ConstructEvent(payload, signatureHeader, whSecret)
	if err != nil {
		log.Errorf("stripe webhook: webhook signature verification failed. %s", err.Error())
		return nil, err
	}
	return &event, nil
}

func GetInvoiceInfoFromEvent(event stripeapi.Event) (time.Time, string, error) {
	var invoice stripeapi.Invoice
	err := json.Unmarshal(event.Data.Raw, &invoice)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("error parsing webhook JSON: %v", err)
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
func GetSubscriptionInfoFromEvent(event stripeapi.Event) (*SubscriptionInfo, error) {
	var subscription stripeapi.Subscription
	err := json.Unmarshal(event.Data.Raw, &subscription)
	if err != nil {
		return &SubscriptionInfo{}, fmt.Errorf("error parsing webhook JSON: %v", err)
	}

	params := &stripeapi.CustomerParams{}
	customer, err := stripeCustomer.Get(subscription.Customer.ID, params)
	if err != nil || customer == nil {
		return &SubscriptionInfo{}, fmt.Errorf(
			"could not update subscription %s, stripe internal error getting customer",
			subscription.ID,
		)
	}
	address := subscription.Metadata["address"]
	if len(address) == 0 {
		return &SubscriptionInfo{}, fmt.Errorf("subscription %s does not contain an address in metadata", subscription.ID)
	}

	if len(subscription.Items.Data) == 0 {
		return &SubscriptionInfo{}, fmt.Errorf("subscription %s does not contain any items", subscription.ID)
	}

	return &SubscriptionInfo{
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
func GetPriceByID(priceID string) *stripeapi.Price {
	params := &stripeapi.PriceSearchParams{
		SearchParams: stripeapi.SearchParams{
			Query: fmt.Sprintf("active:'true' AND lookup_key:'%s'", priceID),
		},
	}
	params.AddExpand("data.tiers")
	if results := stripePrice.Search(params); results.Next() {
		return results.Price()
	}
	return nil
}

// GetProductByID retrieves a Stripe product by its ID.
// It expands the default price and its tiers in the response.
// Returns the product object and any error encountered.
func GetProductByID(productID string) (*stripeapi.Product, error) {
	params := &stripeapi.ProductParams{}
	params.AddExpand("default_price")
	params.AddExpand("default_price.tiers")
	product, err := stripeProduct.Get(productID, params)
	if err != nil {
		return nil, err
	}
	return product, nil
}

// GetPrices retrieves multiple Stripe prices by their IDs.
// It returns a slice of Price objects for all valid price IDs.
// Invalid or non-existent price IDs are silently skipped.
func GetPrices(priceIDs []string) []*stripeapi.Price {
	var prices []*stripeapi.Price
	for _, priceID := range priceIDs {
		if price := GetPriceByID(priceID); price != nil {
			prices = append(prices, price)
		}
	}
	return prices
}

// extractPlanMetadata extracts and parses plan metadata from a Stripe product.
// It handles unmarshaling of organization limits, voting types, and features.
//
//revive:disable:function-result-limit
func extractPlanMetadata(product *stripeapi.Product) (
	stripeDB.PlanLimits, stripeDB.VotingTypes, stripeDB.Features, error,
) {
	var organizationData stripeDB.PlanLimits
	if err := json.Unmarshal([]byte(product.Metadata["organization"]), &organizationData); err != nil {
		return stripeDB.PlanLimits{}, stripeDB.VotingTypes{}, stripeDB.Features{},
			fmt.Errorf("error parsing plan organization metadata JSON: %s", err.Error())
	}

	var votingTypesData stripeDB.VotingTypes
	if err := json.Unmarshal([]byte(product.Metadata["votingTypes"]), &votingTypesData); err != nil {
		return stripeDB.PlanLimits{}, stripeDB.VotingTypes{}, stripeDB.Features{},
			fmt.Errorf("error parsing plan voting types metadata JSON: %s", err.Error())
	}

	var featuresData stripeDB.Features
	if err := json.Unmarshal([]byte(product.Metadata["features"]), &featuresData); err != nil {
		return stripeDB.PlanLimits{}, stripeDB.VotingTypes{}, stripeDB.Features{},
			fmt.Errorf("error parsing plan features metadata JSON: %s", err.Error())
	}

	return organizationData, votingTypesData, featuresData, nil
}

// processPriceTiers converts Stripe price tiers to application-specific plan tiers.
func processPriceTiers(priceTiers []*stripeapi.PriceTier) []stripeDB.PlanTier {
	var tiers []stripeDB.PlanTier
	for _, tier := range priceTiers {
		if tier.UpTo == 0 {
			continue
		}
		tiers = append(tiers, stripeDB.PlanTier{
			Amount: tier.FlatAmount,
			UpTo:   tier.UpTo,
		})
	}
	return tiers
}

// processProduct converts a Stripe product to an application-specific plan.
func processProduct(index int, productID string, product *stripeapi.Product) (*stripeDB.Plan, error) {
	organizationData, votingTypesData, featuresData, err := extractPlanMetadata(product)
	if err != nil {
		return nil, err
	}

	price := product.DefaultPrice
	startingPrice := price.UnitAmount
	if len(price.Tiers) > 0 {
		startingPrice = price.Tiers[0].FlatAmount
	}

	tiers := processPriceTiers(price.Tiers)

	return &stripeDB.Plan{
		ID:              uint64(index),
		Name:            product.Name,
		StartingPrice:   startingPrice,
		StripeID:        productID,
		StripePriceID:   price.ID,
		Default:         price.Metadata["Default"] == "true",
		Organization:    organizationData,
		VotingTypes:     votingTypesData,
		Features:        featuresData,
		CensusSizeTiers: tiers,
	}, nil
}

// GetPlans retrieves and constructs a list of subscription plans from Stripe products.
// It processes product metadata to extract organization limits, voting types, and features.
// Returns a slice of Plan objects and any error encountered during processing.
func GetPlans() ([]*stripeDB.Plan, error) {
	var plans []*stripeDB.Plan

	for i, productID := range ProductsIDs {
		product, err := GetProductByID(productID)
		if err != nil || product == nil {
			return nil, fmt.Errorf("error getting product %s: %s", productID, err.Error())
		}

		plan, err := processProduct(i, productID, product)
		if err != nil {
			return nil, err
		}

		plans = append(plans, plan)
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
func CreateSubscriptionCheckoutSession(
	priceID, returnURL, address, email, locale string, amount int64,
) (*stripeapi.CheckoutSession, error) {
	if len(locale) == 0 {
		locale = "auto"
	}
	if locale == "ca" {
		locale = "es"
	}
	checkoutParams := &stripeapi.CheckoutSessionParams{
		// Subscription mode
		Mode:          stripeapi.String(string(stripeapi.CheckoutSessionModeSubscription)),
		CustomerEmail: &email,
		LineItems: []*stripeapi.CheckoutSessionLineItemParams{
			{
				Price:    stripeapi.String(priceID),
				Quantity: stripeapi.Int64(amount),
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
				"address": address,
			},
		},
		// The locale is being used to configure the language of the embedded client
		Locale: stripeapi.String(locale),
	}

	// The returnURL is used to redirect the user after the payment is completed
	if len(returnURL) > 0 {
		checkoutParams.ReturnURL = stripeapi.String(returnURL + "/{CHECKOUT_SESSION_ID}")
	} else {
		checkoutParams.RedirectOnCompletion = stripeapi.String("never")
	}
	session, err := stripeCheckoutSession.New(checkoutParams)
	if err != nil {
		return nil, err
	}

	return session, nil
}

// RetrieveCheckoutSession retrieves a checkout session from Stripe by session ID.
// It returns a ReturnStatus object and an error if any.
// The ReturnStatus object contains information about the session status,
// customer email, and subscription status.
func RetrieveCheckoutSession(sessionID string) (*ReturnStatus, error) {
	params := &stripeapi.CheckoutSessionParams{}
	params.AddExpand("line_items")
	sess, err := stripeCheckoutSession.Get(sessionID, params)
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
func CreatePortalSession(customerEmail string) (*stripeapi.BillingPortalSession, error) {
	// get stripe customer based on provided email
	customerParams := &stripeapi.CustomerListParams{
		Email: stripeapi.String(customerEmail),
	}
	var customerID string

	customers := stripeCustomer.List(customerParams)
	if !customers.Next() {
		return nil, fmt.Errorf("could not find customer with email %s", customerEmail)
	}
	customerID = customers.Customer().ID

	params := &stripeapi.BillingPortalSessionParams{
		Customer: &customerID,
	}
	return stripePortalSession.New(params)
}
