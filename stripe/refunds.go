package stripe

import (
	"encoding/json"
	"fmt"

	stripeapi "github.com/stripe/stripe-go/v86"
	striperefund "github.com/stripe/stripe-go/v86/refund"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// RefundInfo is the outcome of a refund: its id, and whether Stripe already settled it.
// A refund normally succeeds straight away, but it can stay pending and fail later, which
// arrives as a charge.refund.updated webhook.
type RefundInfo struct {
	ID     string
	Status string
}

// RefundProcessPayment returns the full amount of a process's card payment, VAT included:
// Amount is deliberately left unset, which refunds the whole payment intent rather than the
// net price we quoted. Keyed on the process id, so a delete retried after a timeout returns
// Stripe's original refund instead of issuing a second one.
func (*Service) RefundProcessPayment(processID bson.ObjectID, paymentIntentID string) (*RefundInfo, error) {
	if paymentIntentID == "" {
		return nil, errors.ErrStripeError.Withf("payment has no payment intent to refund")
	}
	params := &stripeapi.RefundParams{
		PaymentIntent: stripeapi.String(paymentIntentID),
		Metadata:      map[string]string{MetadataKeyProcessID: processID.Hex()},
	}
	params.IdempotencyKey = stripeapi.String("refund:" + processID.Hex())
	refund, err := striperefund.New(params)
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to refund payment intent %s: %v", paymentIntentID, err)
	}
	return &RefundInfo{ID: refund.ID, Status: string(refund.Status)}, nil
}

// handleRefundUpdated reacts to a refund that changed state after it was issued. Only a
// refund that will never pay out matters: the money did not go back, so the payment returns
// to paid and stops looking settled. The draft it paid for is already deleted and cannot be
// restored, so this is deliberately a manual-reconciliation signal — logged at error level,
// not quietly absorbed.
func (s *Service) handleRefundUpdated(event *stripeapi.Event) error {
	var refund stripeapi.Refund
	if err := json.Unmarshal(event.Data.Raw, &refund); err != nil {
		return fmt.Errorf("failed to parse refund from event %s: %w", event.ID, err)
	}
	if refund.Status != stripeapi.RefundStatusFailed && refund.Status != stripeapi.RefundStatusCanceled {
		return nil // still pending, or succeeded as expected
	}
	rawProcessID := refund.Metadata[MetadataKeyProcessID]
	processID, err := bson.ObjectIDFromHex(rawProcessID)
	if err != nil {
		return fmt.Errorf("invalid process id %q in refund %s metadata (%v): %w",
			rawProcessID, refund.ID, err, errPermanentEvent)
	}
	reverted, err := s.db.MarkProcessPaymentRefundFailed(processID, refund.ID)
	if err != nil {
		return fmt.Errorf("failed to mark process payment refund failed: %w", err)
	}
	if !reverted {
		return nil // a replay, or this refund is no longer the payment's refund
	}
	log.Errorw(fmt.Errorf("refund %s of %d cents ended %s", refund.ID, refund.Amount, refund.Status),
		fmt.Sprintf("the deleted process %s was not refunded and needs manual reconciliation", processID.Hex()))
	return nil
}
