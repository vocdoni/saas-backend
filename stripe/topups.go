package stripe

import (
	"fmt"
	"slices"
	"strconv"

	stripeapi "github.com/stripe/stripe-go/v86"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// raiseProcessPayment applies verified census headroom: the process's paid envelope is
// raised to the target amount the session carries, which is what the census-growth guard
// compares against. The raise is monotonic and records the session, so a replayed event
// raises nothing.
func (s *Service) raiseProcessPayment(session *stripeapi.CheckoutSession) error {
	rawProcessID := session.Metadata[MetadataKeyProcessID]
	processID, err := bson.ObjectIDFromHex(rawProcessID)
	if err != nil {
		return fmt.Errorf("invalid process id %q in census top-up session %s metadata (%v): %w",
			rawProcessID, session.ID, err, errPermanentEvent)
	}
	rawTarget := session.Metadata[MetadataKeyProcessTopUpCents]
	targetCents, err := strconv.ParseInt(rawTarget, 10, 64)
	if err != nil || targetCents <= 0 {
		return fmt.Errorf("invalid top-up target %q in session %s metadata: %w",
			rawTarget, session.ID, errPermanentEvent)
	}
	if session.PaymentStatus != stripeapi.CheckoutSessionPaymentStatusPaid {
		// a delayed payment method still clearing: the envelope stays where it is, so the
		// census stays refused until async_payment_succeeded arrives
		return nil
	}
	var paymentIntentID string
	if session.PaymentIntent != nil {
		paymentIntentID = session.PaymentIntent.ID
	}
	raised, err := s.db.RaiseProcessPaymentAmount(processID, targetCents, session.ID, paymentIntentID)
	if err != nil {
		return fmt.Errorf("failed to raise process payment amount: %w", err)
	}
	if !raised {
		return s.refundUnappliedTopUp(processID, session, paymentIntentID)
	}
	log.Infow("census headroom paid",
		"processId", processID.Hex(), "targetCents", targetCents, "sessionId", session.ID)
	return nil
}

// refundUnappliedTopUp handles a paid census top-up that raised nothing. A replay of one that
// already raised the envelope is fine. Anything else is money that bought nothing — the draft
// was deleted and refunded while the customer was paying, or a larger top-up landed first —
// so it goes straight back, keyed per intent like every other refund of the process, which
// keeps a replay of this event from refunding twice.
func (s *Service) refundUnappliedTopUp(
	processID bson.ObjectID, session *stripeapi.CheckoutSession, paymentIntentID string,
) error {
	payment, err := s.db.ProcessPayment(processID)
	switch {
	case err == nil && payment.Status == db.ProcessPaymentPaid &&
		(slices.Contains(payment.TopUpSessions, session.ID) || slices.Contains(payment.TopUpIntents, paymentIntentID)):
		return nil // a replay of a top-up already applied
	case err != nil && !errors.Is(err, db.ErrNotFound):
		return fmt.Errorf("failed to read process payment: %w", err)
	}
	if paymentIntentID == "" {
		return fmt.Errorf("census top-up session %s for process %s bought nothing and has no payment intent"+
			" to refund — needs manual reconciliation: %w", session.ID, processID.Hex(), errPermanentEvent)
	}
	refund, err := s.RefundProcessPayment(processID, paymentIntentID, 0)
	if err != nil {
		return fmt.Errorf("failed to refund unapplied census top-up: %w", err)
	}
	log.Warnw("census top-up bought nothing and was refunded",
		"processId", processID.Hex(), "sessionId", session.ID, "amountCents", session.AmountTotal,
		"refundId", refund.ID)
	return nil
}
