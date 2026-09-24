package stripe

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v86"
	stripecheckoutsession "github.com/stripe/stripe-go/v86/checkout/session"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// One-time (mode "payment") checkout sessions for the pay-per-process billing model:
// process purchases and integrator wallet top-ups. Unlike the subscription flow, the
// org/process identity travels in session-level metadata (a payment session has no
// SubscriptionData), and fulfillment happens exclusively through verified webhooks —
// a browser redirect is never proof of payment.

// Session metadata keys that route webhook fulfillment.
const (
	// MetadataKeyProcessID marks a session as the purchase of one voting process.
	MetadataKeyProcessID = "voting_process_id"
	// MetadataKeyWalletTopUpOrg marks a session as a wallet top-up for an integrator
	// organization (value: the org address).
	MetadataKeyWalletTopUpOrg = "wallet_topup_org"
	// MetadataKeyRequestedBy carries the email of the user who started the checkout.
	MetadataKeyRequestedBy = "requested_by"
)

// PaymentLineItem is one server-priced line of a one-time purchase.
type PaymentLineItem struct {
	Description string
	AmountCents int64
}

// PaymentSessionParams packs everything needed to open a one-time checkout session.
type PaymentSessionParams struct {
	LineItems     []PaymentLineItem
	Metadata      map[string]string
	OrgAddress    string // used to reuse the organization's Stripe customer
	CustomerEmail string // fallback identity when no customer exists yet
	ReturnURL     string
	Locale        string
}

// CreatePaymentCheckoutSession opens an embedded one-time checkout session with
// server-calculated line items (tax_behavior exclusive — Stripe Tax adds VAT on top),
// reusing the organization's Stripe customer when one exists. The caller gets the
// session back and must persist its ID before handing the client secret to the UI.
func (c *Client) CreatePaymentCheckoutSession(params *PaymentSessionParams) (*stripeapi.CheckoutSession, error) {
	checkoutParams := buildPaymentSessionParams(params)

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

	session, err := stripecheckoutsession.New(checkoutParams)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to create payment checkout session: %v", err)
	}
	return session, nil
}

// ExpireCheckoutSession expires an open checkout session so an obsolete one (the draft's
// price changed) can be replaced without ever having two payable sessions at once.
// Completed sessions cannot be expired; Stripe answers an error the caller must treat as
// "reconcile first, do not create a replacement".
func (*Client) ExpireCheckoutSession(sessionID string) error {
	if _, err := stripecheckoutsession.Expire(sessionID, &stripeapi.CheckoutSessionExpireParams{}); err != nil {
		return errors.ErrStripeError.Withf("failed to expire checkout session: %v", err)
	}
	return nil
}

// buildPaymentSessionParams maps PaymentSessionParams onto the Stripe request. Pure, so
// the exact wire shape (mode, tax config, price_data) is unit-testable.
func buildPaymentSessionParams(params *PaymentSessionParams) *stripeapi.CheckoutSessionParams {
	lineItems := make([]*stripeapi.CheckoutSessionLineItemParams, 0, len(params.LineItems))
	for _, item := range params.LineItems {
		lineItems = append(lineItems, &stripeapi.CheckoutSessionLineItemParams{
			Quantity: new(int64(1)),
			PriceData: &stripeapi.CheckoutSessionLineItemPriceDataParams{
				Currency:    new("eur"),
				UnitAmount:  new(item.AmountCents),
				TaxBehavior: new("exclusive"),
				ProductData: &stripeapi.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: new(item.Description),
				},
			},
		})
	}
	checkoutParams := &stripeapi.CheckoutSessionParams{
		Mode:      new(string(stripeapi.CheckoutSessionModePayment)),
		LineItems: lineItems,
		// embedded client, same as the subscription flow
		UIMode: new(string(stripeapi.CheckoutSessionUIModeElements)),
		AutomaticTax: &stripeapi.CheckoutSessionAutomaticTaxParams{
			Enabled: new(true),
		},
		TaxIDCollection: &stripeapi.CheckoutSessionTaxIDCollectionParams{
			Enabled: new(true),
		},
		BillingAddressCollection: new(string(stripeapi.CheckoutSessionBillingAddressCollectionAuto)),
		// one-time purchases get a real invoice instead of the subscription invoice flow
		InvoiceCreation: &stripeapi.CheckoutSessionInvoiceCreationParams{
			Enabled: new(true),
		},
		// session-level metadata: a payment session has no SubscriptionData, and the
		// webhook needs the routing identity back from the session itself
		Metadata: params.Metadata,
		Locale:   new(stripeLocale(params.Locale)),
	}
	if params.ReturnURL != "" {
		checkoutParams.ReturnURL = new(params.ReturnURL + "/{CHECKOUT_SESSION_ID}")
	}
	return checkoutParams
}

// SessionStatus and PaymentStatus are the lifecycle states of a checkout session, named
// here so callers outside this package compare against constants instead of literals
// without importing stripe-go. The values are taken from the stripe-go enums, so there is
// no second list to keep in sync.
type (
	SessionStatus string
	PaymentStatus string
)

const (
	// SessionStatusOpen means the session is still payable by the client.
	SessionStatusOpen = SessionStatus(stripeapi.CheckoutSessionStatusOpen)
	// SessionStatusComplete means the client finished the session; the payment may still
	// be processing, so PaymentStatus decides whether money actually arrived.
	SessionStatusComplete = SessionStatus(stripeapi.CheckoutSessionStatusComplete)
	// SessionStatusExpired means the session can no longer be paid and must be replaced.
	SessionStatusExpired = SessionStatus(stripeapi.CheckoutSessionStatusExpired)

	// PaymentStatusPaid means the funds are captured; this is the only proof of payment.
	PaymentStatusPaid = PaymentStatus(stripeapi.CheckoutSessionPaymentStatusPaid)
	// PaymentStatusUnpaid means no money has been captured (yet).
	PaymentStatusUnpaid = PaymentStatus(stripeapi.CheckoutSessionPaymentStatusUnpaid)
	// PaymentStatusNoPaymentRequired means the session totalled zero.
	PaymentStatusNoPaymentRequired = PaymentStatus(stripeapi.CheckoutSessionPaymentStatusNoPaymentRequired)
)

// PaymentSessionInfo is the API-facing view of a one-time checkout session: enough to
// hand the embedded client its secret and to decide reuse/expire/replace.
type PaymentSessionInfo struct {
	ID            string
	ClientSecret  string
	Status        SessionStatus
	PaymentStatus PaymentStatus
}

func paymentSessionInfo(session *stripeapi.CheckoutSession) *PaymentSessionInfo {
	return &PaymentSessionInfo{
		ID:            session.ID,
		ClientSecret:  session.ClientSecret,
		Status:        SessionStatus(session.Status),
		PaymentStatus: PaymentStatus(session.PaymentStatus),
	}
}

// CreatePaymentSession opens a one-time checkout session (see the client method for the
// wire shape) and returns its API-facing view.
func (s *Service) CreatePaymentSession(params *PaymentSessionParams) (*PaymentSessionInfo, error) {
	session, err := s.client.CreatePaymentCheckoutSession(params)
	if err != nil {
		return nil, err
	}
	return paymentSessionInfo(session), nil
}

// GetPaymentSession retrieves a one-time checkout session's current state.
func (*Service) GetPaymentSession(sessionID string) (*PaymentSessionInfo, error) {
	session, err := stripecheckoutsession.Get(sessionID, &stripeapi.CheckoutSessionParams{})
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to get checkout session: %v", err)
	}
	return paymentSessionInfo(session), nil
}

// ExpirePaymentSession expires an open one-time checkout session.
func (s *Service) ExpirePaymentSession(sessionID string) error {
	return s.client.ExpireCheckoutSession(sessionID)
}

// handleCheckoutSessionResult processes checkout.session.completed and
// checkout.session.async_payment_succeeded. Sessions from the subscription flow carry
// none of our metadata keys and are ignored. Fulfillment is idempotent end to end: the
// payment CAS and the wallet applied-keys guard make replayed events no-ops, so this
// handler never needs an event store.
func (s *Service) handleCheckoutSessionResult(event *stripeapi.Event) error {
	session, err := parseCheckoutSessionFromEvent(event)
	if err != nil {
		return err
	}
	switch {
	case session.Metadata[MetadataKeyProcessID] != "":
		return s.fulfillProcessPayment(session)
	case session.Metadata[MetadataKeyWalletTopUpOrg] != "":
		return s.creditWalletTopUp(session)
	default:
		// a subscription-mode checkout completing; the subscription webhooks own it
		return nil
	}
}

// handleCheckoutSessionFailed processes the two ways a checkout stops short of paying:
// checkout.session.async_payment_failed (a delayed payment did not clear) and
// checkout.session.expired (the customer abandoned it and Stripe closed the session). Both
// return the process payment to the payable (failed) state, which is also what releases the
// branding claim the checkout took — without this an abandoned branded checkout would hold
// the organization's once-only add-on forever. A wallet top-up needs nothing (no balance was
// credited).
func (s *Service) handleCheckoutSessionFailed(event *stripeapi.Event) error {
	session, err := parseCheckoutSessionFromEvent(event)
	if err != nil {
		return err
	}
	rawProcessID := session.Metadata[MetadataKeyProcessID]
	if rawProcessID == "" {
		return nil
	}
	processID, err := bson.ObjectIDFromHex(rawProcessID)
	if err != nil {
		return fmt.Errorf("invalid process id %q in session %s metadata (%v): %w",
			rawProcessID, session.ID, err, errPermanentEvent)
	}
	failed, err := s.db.MarkProcessPaymentFailed(processID, session.ID)
	if err != nil {
		return fmt.Errorf("failed to mark process payment failed: %w", err)
	}
	if failed {
		log.Warnw("process payment is payable again",
			"processId", processID.Hex(), "sessionId", session.ID, "reason", string(event.Type))
	}
	return nil
}

// fulfillProcessPayment marks the process paid and triggers publication. A session that
// completed but is not paid yet (delayed payment method) only advances to processing:
// it must neither publish nor allow a new charge until the async outcome arrives.
func (s *Service) fulfillProcessPayment(session *stripeapi.CheckoutSession) error {
	rawProcessID := session.Metadata[MetadataKeyProcessID]
	processID, err := bson.ObjectIDFromHex(rawProcessID)
	if err != nil {
		return fmt.Errorf("invalid process id %q in session %s metadata (%v): %w",
			rawProcessID, session.ID, err, errPermanentEvent)
	}
	if session.PaymentStatus != stripeapi.CheckoutSessionPaymentStatusPaid {
		if _, err := s.db.MarkProcessPaymentProcessing(processID, session.ID); err != nil {
			return fmt.Errorf("failed to mark process payment processing: %w", err)
		}
		return nil
	}
	// the payment intent is what a refund is issued against; the webhook carries it as a
	// bare id, which unmarshals into PaymentIntent.ID without an Expand
	var paymentIntentID string
	if session.PaymentIntent != nil {
		paymentIntentID = session.PaymentIntent.ID
	}
	won, err := s.db.MarkProcessPaymentPaid(processID, session.ID, db.ProcessCharge{
		PaymentIntentID: paymentIntentID,
		SubtotalCents:   session.AmountSubtotal,
		TotalCents:      session.AmountTotal,
	})
	if err != nil {
		return fmt.Errorf("failed to mark process payment paid: %w", err)
	}
	if !won {
		// either a replayed event (payment already paid — fine) or money arrived for a
		// session the process no longer references — that one must not pass silently
		payment, err := s.db.ProcessPayment(processID)
		if err != nil || payment.Status != db.ProcessPaymentPaid || payment.CheckoutSessionID != session.ID {
			return fmt.Errorf("payment received for session %s, which process %s does not reference"+
				" — needs manual reconciliation: %w", session.ID, rawProcessID, errPermanentEvent)
		}
		// A replay of a payment this service already recorded. Run the side effects
		// again rather than returning: the CAS only proves the status was written, not
		// that anything after it ran, and a crash (or a full tx queue) between the two
		// would otherwise strand a paid process unpublished forever — Stripe's retry is
		// the only thing that ever comes back for it. Both side effects are idempotent.
		s.afterProcessPaid(processID)
		return nil
	}
	log.Infow("process payment fulfilled", "processId", processID.Hex(), "sessionId", session.ID)
	s.afterProcessPaid(processID)
	return nil
}

// afterProcessPaid runs the side effects of a paid process payment: stamp the
// once-per-organization branding add-on (conditional in the DB, so only the first payment
// sets it) and hand the process to publication. Idempotent, so it is safe on a webhook
// replay of a payment already recorded as paid.
func (s *Service) afterProcessPaid(processID bson.ObjectID) {
	// the payment record, not the draft, says whether branding was charged: a draft may
	// select branding and still be quoted without it, because another process holds the
	// organization's claim on the add-on.
	if payment, err := s.db.ProcessPayment(processID); err == nil && payment.Branding {
		if _, err := s.db.SetOrganizationBrandingPaid(payment.OrgAddress, time.Now()); err != nil {
			log.Warnw("could not stamp organization branding-paid",
				"processId", processID.Hex(), "orgAddress", payment.OrgAddress.String(), "error", err)
		}
	}
	if s.OnProcessPaid != nil {
		s.OnProcessPaid(processID)
	}
}

// creditWalletTopUp credits a verified top-up to the integrator's wallet. The net
// (pre-tax) amount is what was purchased; VAT goes to the tax authority, not the
// balance. The session id is the idempotency key, so replays cannot double-credit.
func (s *Service) creditWalletTopUp(session *stripeapi.CheckoutSession) error {
	if session.PaymentStatus != stripeapi.CheckoutSessionPaymentStatusPaid {
		// delayed payment method still clearing: credit nothing until it does
		return nil
	}
	rawOrg := session.Metadata[MetadataKeyWalletTopUpOrg]
	if !common.IsHexAddress(rawOrg) {
		return fmt.Errorf("invalid organization address %q in session %s metadata: %w",
			rawOrg, session.ID, errPermanentEvent)
	}
	orgAddress := common.HexToAddress(rawOrg)
	if err := s.db.CreditWallet(db.WalletCredit{
		OrgAddress:     orgAddress,
		AmountCents:    session.AmountSubtotal,
		IdempotencyKey: session.ID,
	}); err != nil {
		return fmt.Errorf("failed to credit wallet top-up: %w", err)
	}
	log.Infow("wallet top-up credited",
		"orgAddress", orgAddress.String(), "amountCents", session.AmountSubtotal, "sessionId", session.ID)
	return nil
}

// parseCheckoutSessionFromEvent extracts the checkout session object from a webhook event.
func parseCheckoutSessionFromEvent(event *stripeapi.Event) (*stripeapi.CheckoutSession, error) {
	var session stripeapi.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return nil, fmt.Errorf("failed to parse checkout session from event: %w", err)
	}
	return &session, nil
}
