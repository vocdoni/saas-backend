package stripe

import (
	"encoding/json"
	goerrors "errors"
	"fmt"
	"strconv"

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
// net price we quoted. A process can hold several intents — the first payment and each census
// top-up — so the idempotency key names both the process and the intent: a delete retried
// after a timeout gets Stripe's original refund back instead of issuing a second one.
//
// Stripe forgets idempotency keys after 24 hours, so an intent it reports as already refunded
// counts as returned too; the ID is then the intent's, since the original refund is unknown.
//
// amountCents > 0 refunds only that much of the intent (VAT included), for a refund that keeps
// something back; the amount joins the key, since Stripe refuses a key reused with other params.
func (*Service) RefundProcessPayment(
	processID bson.ObjectID, paymentIntentID string, amountCents int64,
) (*RefundInfo, error) {
	if paymentIntentID == "" {
		return nil, errors.ErrStripeError.Withf("payment has no payment intent to refund")
	}
	params := &stripeapi.RefundParams{
		PaymentIntent: stripeapi.String(paymentIntentID),
		Metadata:      map[string]string{MetadataKeyProcessID: processID.Hex()},
	}
	key := "refund:" + processID.Hex() + ":" + paymentIntentID
	if amountCents > 0 {
		params.Amount = stripeapi.Int64(amountCents)
		key += ":" + strconv.FormatInt(amountCents, 10)
	}
	params.IdempotencyKey = stripeapi.String(key)
	refund, err := striperefund.New(params)
	var stripeErr *stripeapi.Error
	if goerrors.As(err, &stripeErr) && stripeErr.Code == stripeapi.ErrorCodeChargeAlreadyRefunded {
		return &RefundInfo{ID: paymentIntentID, Status: string(stripeapi.RefundStatusSucceeded)}, nil
	}
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
		// a replay, or a census top-up's refund: the payment's status names only the first
		// refund, but a top-up that did not come back is money owed all the same
		log.Errorw(fmt.Errorf("refund %s of %d cents ended %s", refund.ID, refund.Amount, refund.Status),
			fmt.Sprintf("process %s may hold unreturned money and needs manual reconciliation", processID.Hex()))
		return nil
	}
	log.Errorw(fmt.Errorf("refund %s of %d cents ended %s", refund.ID, refund.Amount, refund.Status),
		fmt.Sprintf("the deleted process %s was not refunded and needs manual reconciliation", processID.Hex()))
	return nil
}
