package stripe

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripecheckoutsession "github.com/stripe/stripe-go/v87/checkout/session"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// One-time (mode "payment") checkout sessions for the pay-per-process billing model. Every one
// is a wallet top-up: a process is always paid from a wallet, and the card only fills it. The
// wallet (and, for a top-up opened for a process, the process) travels in session-level
// metadata, copied onto the payment intent so a refund can be traced back to the wallet, and
// fulfillment happens exclusively through verified webhooks — a browser redirect is never
// proof of payment.

// Session metadata keys that route webhook fulfillment.
const (
	// MetadataKeyWalletTopUpOrg marks a session as a wallet top-up (value: the wallet's
	// organization address). Every payment session carries it, and it routes the webhook.
	MetadataKeyWalletTopUpOrg = "wallet_topup_org"
	// MetadataKeyProcessID marks a top-up as opened for one voting process: once credited, the
	// wallet pays that process's publish (or census headroom) right away.
	MetadataKeyProcessID = "voting_process_id"
	// MetadataKeyProcessTopUpCents marks a process top-up as census headroom, carrying the
	// *target* envelope the process must be raised to (not the difference charged).
	MetadataKeyProcessTopUpCents = "process_topup_cents"
	// MetadataKeyProcessTopUpFromCents carries the envelope the headroom was priced from: it is
	// applied only while the process still paid exactly that, so a stale session never buys
	// headroom from a base it did not price.
	MetadataKeyProcessTopUpFromCents = "process_topup_from_cents"
	// MetadataKeyRequestedBy carries the email of the user who started the checkout.
	MetadataKeyRequestedBy = "requested_by"
)

// PaymentLineItem is one server-priced line of a one-time purchase.
type PaymentLineItem struct {
	Description string
	AmountCents pricing.Cents
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
				UnitAmount:  new(int64(item.AmountCents)),
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
		// and on the payment intent, which is what a refund made in the dashboard names
		PaymentIntentData: &stripeapi.CheckoutSessionPaymentIntentDataParams{Metadata: params.Metadata},
		Locale:            new(stripeLocale(params.Locale)),
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
// checkout.session.async_payment_succeeded. It routes on the wallet first: every payment session
// is a wallet top-up, and the process it may name only selects the follow-up. Sessions from the
// subscription flow carry none of our metadata keys and are ignored.
func (s *Service) handleCheckoutSessionResult(event *stripeapi.Event) error {
	session, err := parseCheckoutSessionFromEvent(event)
	if err != nil {
		return err
	}
	if session.Metadata[MetadataKeyWalletTopUpOrg] == "" {
		// a subscription-mode checkout completing; the subscription webhooks own it
		return nil
	}
	return s.creditWalletTopUp(session)
}

// handleCheckoutSessionFailed processes the two ways a checkout stops short of paying:
// checkout.session.async_payment_failed (a delayed payment did not clear) and
// checkout.session.expired (the customer abandoned it and Stripe closed the session). Nothing
// was credited. A top-up opened for a process drops the process's pending payment, which also
// releases the add-on claims the checkout took — without this an abandoned checkout would hold
// the organization's once-per-organization add-ons until the claims went stale.
func (s *Service) handleCheckoutSessionFailed(event *stripeapi.Event) error {
	session, err := parseCheckoutSessionFromEvent(event)
	if err != nil {
		return err
	}
	if session.Metadata[MetadataKeyWalletTopUpOrg] == "" || session.Metadata[MetadataKeyProcessID] == "" {
		return nil
	}
	processID, err := sessionProcessID(session)
	if err != nil {
		return err
	}
	if s.OnProcessCheckoutDropped == nil {
		return nil
	}
	if err := s.OnProcessCheckoutDropped(processID, session.ID); err != nil {
		return fmt.Errorf("failed to drop the checkout of process %s: %w", processID.Hex(), err)
	}
	return nil
}

// sessionProcessID parses the process a top-up session was opened for.
func sessionProcessID(session *stripeapi.CheckoutSession) (bson.ObjectID, error) {
	raw := session.Metadata[MetadataKeyProcessID]
	processID, err := bson.ObjectIDFromHex(raw)
	if err != nil {
		return bson.NilObjectID, fmt.Errorf("invalid process id %q in session %s metadata (%v): %w",
			raw, session.ID, err, errPermanentEvent)
	}
	return processID, nil
}

// netPaidCents is what a session's items were paid for, VAT excluded: the subtotal, which
// Stripe reports before discounts, less any discount.
func netPaidCents(session *stripeapi.CheckoutSession) pricing.Cents {
	if session.TotalDetails == nil {
		return pricing.Cents(session.AmountSubtotal)
	}
	return pricing.Cents(session.AmountSubtotal - session.TotalDetails.AmountDiscount)
}

// ProcessTopUp is a credited top-up that was opened for a process: the wallet now pays its
// publish (HeadroomCents zero) or raises its census envelope from FromCents to HeadroomCents.
type ProcessTopUp struct {
	ProcessID     bson.ObjectID
	SessionID     string
	HeadroomCents pricing.Cents
	FromCents     pricing.Cents
}

// creditWalletTopUp credits a verified top-up to the wallet named in the session, then hands a
// top-up opened for a process to OnProcessTopUp. The net (pre-tax, after any discount) amount is
// what was paid for; VAT goes to the tax authority, not the balance. The session id is the
// idempotency key, so replays cannot double-credit; the follow-up runs on every delivery and is
// idempotent on its own.
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
	var paymentIntentID string
	if session.PaymentIntent != nil {
		paymentIntentID = session.PaymentIntent.ID
	}
	// a session discounted to nothing net credits nothing (and would be refused as invalid)
	if net := netPaidCents(session); net > 0 {
		if err := s.db.CreditWallet(db.WalletCredit{
			OrgAddress:      orgAddress,
			AmountCents:     net,
			IdempotencyKey:  session.ID,
			Kind:            db.WalletEntryTopUp,
			PaymentIntentID: paymentIntentID,
			GrossCents:      pricing.Cents(session.AmountTotal),
		}); err != nil {
			return fmt.Errorf("failed to credit wallet top-up: %w", err)
		}
		log.Infow("wallet top-up credited",
			"orgAddress", orgAddress.String(), "amountCents", net, "sessionId", session.ID)
	}
	if session.Metadata[MetadataKeyProcessID] == "" || s.OnProcessTopUp == nil {
		return nil
	}
	// parsed after the credit: metadata that cannot name a process still paid for the credit
	topUp, err := sessionProcessTopUp(session)
	if err != nil {
		return err
	}
	if err := s.OnProcessTopUp(*topUp); err != nil {
		return fmt.Errorf("failed to apply the top-up of process %s: %w", topUp.ProcessID.Hex(), err)
	}
	return nil
}

// sessionProcessTopUp parses the process (and census headroom) a top-up session was opened for.
func sessionProcessTopUp(session *stripeapi.CheckoutSession) (*ProcessTopUp, error) {
	processID, err := sessionProcessID(session)
	if err != nil {
		return nil, err
	}
	topUp := &ProcessTopUp{ProcessID: processID, SessionID: session.ID}
	if raw := session.Metadata[MetadataKeyProcessTopUpCents]; raw != "" {
		target, terr := strconv.ParseInt(raw, 10, 64)
		from, ferr := strconv.ParseInt(session.Metadata[MetadataKeyProcessTopUpFromCents], 10, 64)
		if terr != nil || ferr != nil || from < 0 || target <= from {
			return nil, fmt.Errorf("invalid census headroom %q from %q in session %s metadata: %w", raw,
				session.Metadata[MetadataKeyProcessTopUpFromCents], session.ID, errPermanentEvent)
		}
		topUp.HeadroomCents, topUp.FromCents = pricing.Cents(target), pricing.Cents(from)
	}
	return topUp, nil
}

// handleRefund mirrors in the wallet a refund made by hand in the Stripe dashboard (the service
// never refunds): on refund.created and refund.updated, a succeeded refund of a payment intent
// that credited a wallet top-up is debited from that wallet, net of VAT at the top-up's own
// ratio (rounded down), in full even below zero. Keyed by the refund id, so replays, and the
// updated event of a refund already counted when created, debit nothing. A refund of anything
// else — a subscription invoice, a payment intent with no top-up — is ignored.
func (s *Service) handleRefund(event *stripeapi.Event) error {
	var refund stripeapi.Refund
	if err := json.Unmarshal(event.Data.Raw, &refund); err != nil {
		return fmt.Errorf("failed to parse refund from event %s: %w", event.ID, err)
	}
	if refund.Status != stripeapi.RefundStatusSucceeded || refund.Amount <= 0 {
		return nil // pending, failed or canceled: no money left
	}
	if refund.PaymentIntent == nil || refund.PaymentIntent.ID == "" {
		log.Infow("ignoring refund without a payment intent", "refundId", refund.ID)
		return nil
	}
	topUp, err := s.db.WalletTopUpByPaymentIntent(refund.PaymentIntent.ID)
	if errors.Is(err, db.ErrNotFound) {
		// not a wallet top-up. ponytail: also a top-up refunded before its completed event was
		// credited, which a human in the dashboard cannot realistically do; read the payment
		// intent's wallet_topup_org metadata from Stripe if it ever happens.
		log.Infow("ignoring refund of a payment that is not a credited wallet top-up",
			"refundId", refund.ID, "paymentIntentId", refund.PaymentIntent.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to find the top-up of refund %s: %w", refund.ID, err)
	}
	netCents := pricing.Cents(refund.Amount)
	if topUp.GrossCents > 0 {
		netCents = pricing.Cents(refund.Amount) * topUp.AmountCents / topUp.GrossCents
	}
	if netCents <= 0 {
		return nil
	}
	if err := s.db.DebitWalletForRefund(db.WalletRefund{
		OrgAddress: topUp.OrgAddress, AmountCents: netCents, RefundID: refund.ID,
	}); err != nil {
		return fmt.Errorf("failed to debit refund %s: %w", refund.ID, err)
	}
	log.Warnw("wallet top-up refunded in stripe, debited from the wallet", "orgAddress", topUp.OrgAddress.String(),
		"refundId", refund.ID, "refundedCents", refund.Amount, "debitedCents", netCents)
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
