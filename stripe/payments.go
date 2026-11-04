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
	if err := s.creditTopUpSession(session); err != nil {
		return err
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

// creditTopUpSession credits a paid top-up session's net amount to the wallet its metadata names,
// keyed by the session id: crediting it again, from any event, is a no-op. A refund handled before
// the completed event credits through it alone (topUpOfPaymentIntent): publishing a process from
// money already returned is left to the completed event, which finds it gone.
func (s *Service) creditTopUpSession(session *stripeapi.CheckoutSession) error {
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
// that credited a wallet top-up is debited from that wallet (debitTopUpRefund). Keyed by the refund
// id, so replays, and the updated event of a refund already counted when created, debit nothing.
// A refund that later fails or is canceled once debited is credited back, keyed
// "refundfail:<refund id>"; one never debited changes nothing. A refund of anything else — a
// subscription invoice, a payment intent with no top-up — is ignored.
//
// Events can arrive out of order (a first delivery that failed is retried later): a failed or
// canceled update handled before the succeeded event it follows finds nothing to reverse. So a
// succeeded event is checked against the refund as Stripe has it now, and debits only while it is
// still succeeded.
func (s *Service) handleRefund(event *stripeapi.Event) error {
	var refund stripeapi.Refund
	if err := json.Unmarshal(event.Data.Raw, &refund); err != nil {
		return fmt.Errorf("failed to parse refund from event %s: %w", event.ID, err)
	}
	switch refund.Status {
	case stripeapi.RefundStatusSucceeded:
		current, err := s.client.GetRefund(refund.ID)
		if err != nil {
			return fmt.Errorf("failed to re-read refund %s: %w", refund.ID, err)
		}
		if current.Status != stripeapi.RefundStatusSucceeded {
			log.Infow("refund no longer succeeded, nothing to debit", "refundId", refund.ID, "status", current.Status)
			return nil
		}
		return s.debitTopUpRefund(refund.PaymentIntent, refund.Amount, "striperefund:"+refund.ID)
	case stripeapi.RefundStatusFailed, stripeapi.RefundStatusCanceled:
		return s.reverseTopUpRefund(refund.PaymentIntent, "striperefund:"+refund.ID, "refundfail:"+refund.ID)
	default:
		return nil // pending or requires_action: no money moved yet
	}
}

// handleDispute mirrors in the wallet a dispute (chargeback) on a top-up: the n-th withdrawal of
// its funds is debited like a refund (debitTopUpRefund), keyed "dispute:<dispute id>:<n>", even
// below zero, and the n-th reinstatement (a won dispute) credits that debit back, keyed
// "disputereinstated:<dispute id>:<n>". The dispute fee Stripe charges is not the payer's money
// and is not mirrored.
//
// Events can arrive out of order (a first delivery that failed is retried later): a reinstatement
// handled before the withdrawal it undoes finds nothing to credit back. So either event re-reads
// the dispute from Stripe and applies every movement its balance transactions list now: a
// withdrawal already reinstated is credited back if it was debited and never debited otherwise,
// any other is debited. Each movement is keyed, so any delivery order and replays end the same.
func (s *Service) handleDispute(event *stripeapi.Event) error {
	var dispute stripeapi.Dispute
	if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
		return fmt.Errorf("failed to parse dispute from event %s: %w", event.ID, err)
	}
	current, err := s.client.GetDispute(dispute.ID)
	if err != nil {
		return fmt.Errorf("failed to re-read dispute %s: %w", dispute.ID, err)
	}
	withdrawn, reinstated := disputeMovements(current)
	if event.Type == stripeapi.EventTypeChargeDisputeFundsWithdrawn {
		withdrawn = max(withdrawn, 1) // a dispute that lists no movement yet: this event is the first
	}
	for n := 1; n <= withdrawn; n++ {
		debitKey := fmt.Sprintf("dispute:%s:%d", dispute.ID, n)
		if n <= reinstated {
			creditKey := fmt.Sprintf("disputereinstated:%s:%d", dispute.ID, n)
			err = s.reverseTopUpRefund(current.PaymentIntent, debitKey, creditKey)
		} else {
			err = s.debitTopUpRefund(current.PaymentIntent, current.Amount, debitKey)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// disputeMovements counts the withdrawals (negative) and reinstatements (positive) among the
// balance transactions of a dispute. Funds are reinstated only after they were withdrawn, so the
// n-th reinstatement undoes the n-th withdrawal.
func disputeMovements(dispute *stripeapi.Dispute) (withdrawn, reinstated int) {
	for _, bt := range dispute.BalanceTransactions {
		switch {
		case bt == nil || bt.Amount == 0:
		case bt.Amount < 0:
			withdrawn++
		default:
			reinstated++
		}
	}
	return withdrawn, reinstated
}

// topUpOfPaymentIntent returns the top-up ledger row a payment intent credited, or db.ErrNotFound
// for a payment that is not a wallet top-up. Funds can be returned before the top-up's completed
// event is handled (the endpoint was failing and Stripe redelivers in any order): the top-up is
// then credited first, from its checkout session and under the session's key, so the completed
// event credits nothing when it lands and the two net to zero whatever the order.
func (s *Service) topUpOfPaymentIntent(paymentIntentID string) (*db.WalletLedgerEntry, error) {
	topUp, err := s.db.WalletTopUpByPaymentIntent(paymentIntentID)
	if !errors.Is(err, db.ErrNotFound) {
		return topUp, err
	}
	session, err := s.client.TopUpSessionByPaymentIntent(paymentIntentID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.PaymentStatus != stripeapi.CheckoutSessionPaymentStatusPaid {
		return nil, db.ErrNotFound
	}
	if err := s.creditTopUpSession(session); err != nil {
		return nil, err
	}
	return s.db.WalletTopUpByPaymentIntent(paymentIntentID)
}

// debitTopUpRefund debits from a wallet amount (VAT included) that Stripe returned to the payer of
// the top-up paid through paymentIntent — found through the top-up's ledger row, credited first if
// it was not yet (topUpOfPaymentIntent) — net of VAT at the top-up's own ratio (rounded down), in
// full even below zero. Money returned on a payment that is not a top-up is ignored.
func (s *Service) debitTopUpRefund(paymentIntent *stripeapi.PaymentIntent, amount int64, key string) error {
	if amount <= 0 {
		return nil
	}
	if paymentIntent == nil || paymentIntent.ID == "" {
		log.Infow("ignoring returned funds without a payment intent", "key", key)
		return nil
	}
	topUp, err := s.topUpOfPaymentIntent(paymentIntent.ID)
	if errors.Is(err, db.ErrNotFound) {
		log.Infow("ignoring returned funds of a payment that is not a wallet top-up",
			"key", key, "paymentIntentId", paymentIntent.ID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to find the top-up of %s: %w", key, err)
	}
	netCents := pricing.Cents(amount)
	if topUp.GrossCents > 0 {
		netCents = pricing.Cents(amount) * topUp.AmountCents / topUp.GrossCents
	}
	if netCents <= 0 {
		return nil
	}
	if err := s.db.DebitWalletForRefund(db.WalletRefund{
		OrgAddress: topUp.OrgAddress, AmountCents: netCents, Key: key,
	}); err != nil {
		return fmt.Errorf("failed to debit %s: %w", key, err)
	}
	log.Warnw("wallet top-up returned through stripe, debited from the wallet", "orgAddress", topUp.OrgAddress.String(),
		"key", key, "returnedCents", amount, "debitedCents", netCents)
	return nil
}

// reverseTopUpRefund credits back the debit keyed debitKey, if one was recorded in the wallet of
// the top-up paid through paymentIntent.
func (s *Service) reverseTopUpRefund(paymentIntent *stripeapi.PaymentIntent, debitKey, creditKey string) error {
	if paymentIntent == nil || paymentIntent.ID == "" {
		log.Infow("ignoring undone funds without a payment intent", "key", debitKey)
		return nil
	}
	topUp, err := s.topUpOfPaymentIntent(paymentIntent.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil // not a wallet top-up: nothing was debited
	}
	if err != nil {
		return fmt.Errorf("failed to find the top-up of %s: %w", debitKey, err)
	}
	reversed, err := s.db.ReverseWalletRefund(topUp.OrgAddress, debitKey, creditKey)
	if err != nil {
		return fmt.Errorf("failed to credit back %s: %w", debitKey, err)
	}
	if !reversed {
		log.Infow("stripe undid returned funds that were never debited, nothing to credit back", "key", debitKey)
		return nil
	}
	log.Warnw("stripe undid returned funds, credited back to the wallet", "debitKey", debitKey, "creditKey", creditKey)
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
