package stripe

import (
	stderrors "errors"
	"fmt"

	stripeapi "github.com/stripe/stripe-go/v87"
	stripecheckoutsession "github.com/stripe/stripe-go/v87/checkout/session"
	stripecustomer "github.com/stripe/stripe-go/v87/customer"
	stripedispute "github.com/stripe/stripe-go/v87/dispute"
	stripeinvoice "github.com/stripe/stripe-go/v87/invoice"
	stripeinvoicepayment "github.com/stripe/stripe-go/v87/invoicepayment"
	striperefund "github.com/stripe/stripe-go/v87/refund"
	stripewebhook "github.com/stripe/stripe-go/v87/webhook"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
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

// GetRefund retrieves a refund by ID, with its current status.
func (*Client) GetRefund(refundID string) (*stripeapi.Refund, error) {
	refund, err := striperefund.Get(refundID, nil)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get refund: %v", err)
	}
	return refund, nil
}

// GetDispute retrieves a dispute by ID, with the balance transactions that moved its funds so far.
func (*Client) GetDispute(disputeID string) (*stripeapi.Dispute, error) {
	dispute, err := stripedispute.Get(disputeID, nil)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get dispute: %v", err)
	}
	return dispute, nil
}

// GetInvoice retrieves an invoice by ID with its payments and their payment intents expanded:
// in this API version an invoice carries no payment intent of its own, and payments is only
// returned on request.
func (*Client) GetInvoice(invoiceID string) (*stripeapi.Invoice, error) {
	params := &stripeapi.InvoiceParams{}
	params.AddExpand("payments.data.payment.payment_intent")
	invoice, err := stripeinvoice.Get(invoiceID, params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get invoice: %v", err)
	}
	return invoice, nil
}

// InvoiceByPaymentIntent returns the id of the invoice the payment intent paid, or "" when it
// paid none (any payment not made against an invoice).
func (*Client) InvoiceByPaymentIntent(paymentIntentID string) (string, error) {
	iter := stripeinvoicepayment.List(&stripeapi.InvoicePaymentListParams{
		Status: new("paid"),
		Payment: &stripeapi.InvoicePaymentListPaymentParams{
			Type:          new(string(stripeapi.InvoicePaymentPaymentTypePaymentIntent)),
			PaymentIntent: new(paymentIntentID),
		},
	})
	for iter.Next() {
		if payment := iter.InvoicePayment(); payment.Invoice != nil && payment.Invoice.ID != "" {
			return payment.Invoice.ID, nil
		}
	}
	if err := iter.Err(); err != nil {
		return "", errors.ErrStripeError.Withf("failed to list invoice payments: %v", err)
	}
	return "", nil
}

// TopUpSessionByPaymentIntent returns the wallet top-up checkout session that created the
// payment intent, or nil when no top-up session did (any other payment).
func (*Client) TopUpSessionByPaymentIntent(paymentIntentID string) (*stripeapi.CheckoutSession, error) {
	iter := stripecheckoutsession.List(&stripeapi.CheckoutSessionListParams{
		PaymentIntent: stripeapi.String(paymentIntentID),
	})
	for iter.Next() {
		if session := iter.CheckoutSession(); session.Metadata[MetadataKeyWalletTopUpOrg] != "" {
			return session, nil
		}
	}
	if err := iter.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to list checkout sessions: %v", err)
	}
	return nil, nil
}

// CheckoutSessionsOfCustomer returns every checkout session of a customer, of any mode and
// status, oldest last as Stripe lists them; none is an empty slice, not an error.
func (*Client) CheckoutSessionsOfCustomer(customerID string) ([]*stripeapi.CheckoutSession, error) {
	var sessions []*stripeapi.CheckoutSession
	iter := stripecheckoutsession.List(&stripeapi.CheckoutSessionListParams{
		Customer: stripeapi.String(customerID),
	})
	for iter.Next() {
		sessions = append(sessions, iter.CheckoutSession())
	}
	if err := iter.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to list the checkout sessions of customer %s: %v", customerID, err)
	}
	return sessions, nil
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

// ErrCustomerNotFound is GetCustomerByAddress finding no customer stamped with the address;
// a failed search (network, rate limit, outage) is errors.ErrStripeError instead.
var ErrCustomerNotFound = fmt.Errorf("customer not found")

// GetCustomerByAddress retrieves a customer by the organization EVM address: one stamped with
// it in metadata.address. No such customer wraps ErrCustomerNotFound.
func (*Client) GetCustomerByAddress(address string) (*stripeapi.Customer, error) {
	customers := stripecustomer.Search(&stripeapi.CustomerSearchParams{
		SearchParams: stripeapi.SearchParams{
			Query: fmt.Sprintf("metadata['address']:'%s'", address),
			Limit: new(int64(1)),
		},
	})
	if customers.Next() {
		return customers.Customer(), nil
	}
	if err := customers.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to search customers: %v", err)
	}
	return nil, fmt.Errorf("customer with address %s: %w", address, ErrCustomerNotFound)
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

// GetCheckoutSession retrieves a checkout session by ID
func (*Client) GetCheckoutSession(sessionID string) (*CheckoutSessionStatus, error) {
	params := &stripeapi.CheckoutSessionParams{}
	params.AddExpand("line_items")

	session, err := stripecheckoutsession.Get(sessionID, params)
	if err != nil {
		var stripeErr *stripeapi.Error
		if stderrors.As(err, &stripeErr) && stripeErr.Code == stripeapi.ErrorCodeResourceMissing {
			return nil, errors.ErrCheckoutSessionNotFound
		}
		return nil, errors.ErrStripeError.Withf("failed to get checkout session: %v", err)
	}

	return checkoutSessionStatus(session), nil
}

// checkoutSessionStatus maps a Stripe checkout session onto the API status shape.
// CustomerDetails is nil until the customer fills the form. A malformed pricing breakdown is
// left out and logged at debug level only: this is a polled read, and it still answers.
func checkoutSessionStatus(session *stripeapi.CheckoutSession) *CheckoutSessionStatus {
	status := &CheckoutSessionStatus{
		Status:        string(session.Status),
		PaymentStatus: string(session.PaymentStatus),
	}
	if session.CustomerDetails != nil {
		status.CustomerEmail = session.CustomerDetails.Email
	}
	// a wallet top-up (a process checkout's included) names the wallet's organization
	status.OrgAddress = session.Metadata[MetadataKeyWalletTopUpOrg]
	breakdown, err := sessionPricing(session)
	if err != nil {
		log.Debugw("malformed pricing breakdown on a checkout session, left out of its status",
			"sessionId", session.ID, "error", err.Error())
	}
	status.Pricing = breakdown
	return status
}

// CheckoutSessionStatus represents the status of a checkout session
type CheckoutSessionStatus struct {
	Status        string `json:"status"`
	CustomerEmail string `json:"customer_email"`
	// PaymentStatus is the payment outcome of the session, in every mode: "paid",
	// "unpaid" or "no_payment_required".
	PaymentStatus string `json:"payment_status,omitempty"`
	// OrgAddress is the organization the session was created for, used to authorize who
	// may read the session; it never reaches the wire.
	OrgAddress string `json:"-"`
	// Pricing is the breakdown behind the amount of a top-up opened for a process (a process
	// checkout or census headroom): the quote, the wallet balance and the shortfall it charged.
	// Absent for any other session. Display data only.
	Pricing *pricing.Breakdown `json:"pricing,omitempty"`
}
