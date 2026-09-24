package stripe

import (
	goerrors "errors"
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
// top-up — so a retry must find the refund it already issued for that intent instead of
// issuing a second one. It looks it up first: a live refund of the intent carrying this
// process's metadata is returned as is, with its own ID and status, so a pending one stays
// pending and its failure webhook still matches, and a partial refund is never repeated.
// That lookup holds past Stripe's 24-hour idempotency window; the key, naming process and
// intent, still guards two attempts racing each other within it.
//
// An intent Stripe reports as already refunded by a refund of no process — made from the
// dashboard — counts as returned; the ID is then the intent's, since that refund is not ours.
//
// amountCents > 0 refunds only that much of the intent (VAT included), for a refund that keeps
// something back; the amount joins the key, since Stripe refuses a key reused with other params.
func (*Service) RefundProcessPayment(
	processID bson.ObjectID, paymentIntentID string, amountCents int64,
) (*RefundInfo, error) {
	if paymentIntentID == "" {
		return nil, errors.ErrStripeError.Withf("payment has no payment intent to refund")
	}
	existing, err := processRefund(processID, paymentIntentID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &RefundInfo{ID: existing.ID, Status: string(existing.Status)}, nil
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
		log.Warnw("payment intent was refunded outside the process flow",
			"processId", processID.Hex(), "paymentIntentId", paymentIntentID)
		return &RefundInfo{ID: paymentIntentID, Status: string(stripeapi.RefundStatusSucceeded)}, nil
	}
	if err != nil {
		return nil, errors.ErrStripeError.Withf("failed to refund payment intent %s: %v", paymentIntentID, err)
	}
	return &RefundInfo{ID: refund.ID, Status: string(refund.Status)}, nil
}

// processRefund returns the live refund this process already holds on the intent, or nil. A
// failed or canceled one returned nothing, so it does not count: the retry refunds afresh.
func processRefund(processID bson.ObjectID, paymentIntentID string) (*stripeapi.Refund, error) {
	iter := striperefund.List(&stripeapi.RefundListParams{PaymentIntent: stripeapi.String(paymentIntentID)})
	for iter.Next() {
		r := iter.Refund()
		if r.Metadata[MetadataKeyProcessID] == processID.Hex() &&
			r.Status != stripeapi.RefundStatusFailed && r.Status != stripeapi.RefundStatusCanceled {
			return r, nil
		}
	}
	if err := iter.Err(); err != nil {
		return nil, errors.ErrStripeError.Withf("failed to list refunds of payment intent %s: %v", paymentIntentID, err)
	}
	return nil, nil
}
