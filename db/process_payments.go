package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Every transition here is a conditional single-document write (the filter is the state
// machine), following ClaimVotingProcessForPublish: a call that matched the filter won the
// transition, and MatchedCount — never ModifiedCount — reports it. A duplicate Stripe
// webhook or a concurrent retry therefore resolves to a lost CAS instead of a second side
// effect, which is what makes payment fulfillment idempotent without any event store.

// ProcessPayment returns the payment state of a voting process, or ErrNotFound when the
// process has none (free, or never quoted).
func (ms *MongoStorage) ProcessPayment(processID bson.ObjectID) (*ProcessPayment, error) {
	if processID == bson.NilObjectID {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	payment := &ProcessPayment{}
	if err := ms.processPayments.FindOne(ctx, bson.M{"_id": processID}).Decode(payment); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process payment: %w", err)
	}
	return payment, nil
}

// SetProcessPaymentPending records a fresh open checkout session for a process, replacing
// the payment document. It only succeeds while no payment exists yet or the existing one
// is pending or failed; a processing or paid payment must never be replaced (that would
// allow a second charge), and the call reports false so the caller surfaces the conflict.
//
// replacesSessionID is the checkout session the caller observed and has already expired —
// empty when it saw no payment at all. It is part of the filter, so the replace only
// applies to the state the caller actually reconciled: two concurrent checkouts otherwise
// both succeed, leaving the session the loser opened still open and payable while the
// record points at the winner's. Paying that orphan fulfills nothing.
func (ms *MongoStorage) SetProcessPaymentPending(payment *ProcessPayment, replacesSessionID string) (bool, error) {
	if payment == nil || payment.ProcessID == bson.NilObjectID ||
		(payment.OrgAddress.Cmp(common.Address{}) == 0) || payment.AmountCents <= 0 {
		return false, ErrInvalidData
	}
	now := time.Now()
	payment.Status = ProcessPaymentPending
	payment.PaidAt = time.Time{}
	if payment.CreatedAt.IsZero() {
		payment.CreatedAt = now
	}
	payment.UpdatedAt = now

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":               payment.ProcessID,
		"status":            bson.M{"$in": bson.A{ProcessPaymentPending, ProcessPaymentFailed}},
		"checkoutSessionId": replacesSessionID,
	}
	res, err := ms.processPayments.ReplaceOne(ctx, filter, payment, options.Replace().SetUpsert(true))
	if err != nil {
		// With upsert, an existing document the filter excludes (processing/paid, or a
		// session other than the one being replaced) makes the server attempt an insert
		// that collides on _id: that duplicate key IS the refusal, not a storage failure.
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to set process payment pending: %w", err)
	}
	return res.MatchedCount == 1 || res.UpsertedCount == 1, nil
}

// MarkProcessPaymentProcessing transitions an open session to processing: the customer
// completed checkout with a delayed payment method and the outcome is pending. Only the
// pending payment holding exactly this session may transition.
func (ms *MongoStorage) MarkProcessPaymentProcessing(processID bson.ObjectID, sessionID string) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID,
		bson.A{ProcessPaymentPending}, bson.M{"status": ProcessPaymentProcessing})
}

// MarkProcessPaymentPaid marks a payment as paid, from pending or processing, only when
// the stored checkout session matches the fulfilled one. Paid is terminal for the charge: a
// duplicate webhook (or any replay) no longer matches the filter and reports false, which
// callers use as the signal to skip fulfillment side effects.
//
// charge is what Stripe took, stored because it is the only handle a later refund has on the
// money; an empty field leaves the stored one untouched rather than blanking what an earlier
// attempt recorded.
func (ms *MongoStorage) MarkProcessPaymentPaid(
	processID bson.ObjectID, sessionID string, charge ProcessCharge,
) (bool, error) {
	set := bson.M{"status": ProcessPaymentPaid, "paidAt": time.Now()}
	if charge.PaymentIntentID != "" {
		set["paymentIntentId"] = charge.PaymentIntentID
	}
	if charge.SubtotalCents > 0 && charge.TotalCents > 0 {
		set["chargeSubtotalCents"] = charge.SubtotalCents
		set["chargeTotalCents"] = charge.TotalCents
	}
	return ms.transitionProcessPayment(processID, sessionID,
		bson.A{ProcessPaymentPending, ProcessPaymentProcessing}, set)
}

// MarkProcessPaymentFailed records a failed payment, returning the process to a payable
// state. Only pending or processing payments holding this session may fail; paid never
// regresses.
func (ms *MongoStorage) MarkProcessPaymentFailed(processID bson.ObjectID, sessionID string) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID,
		bson.A{ProcessPaymentPending, ProcessPaymentProcessing},
		bson.M{"status": ProcessPaymentFailed})
}

// SetProcessPaymentPaidByWallet records a wallet-debited payment as paid directly (no
// checkout session). It upserts, so the integrator publish path needs no prior pending
// state, and refuses to touch a payment being charged through Stripe. An already-paid
// wallet payment may be raised to a higher amount — a publish retry after the census grew
// tops up the wallet debit, and this records the new price — but never lowered, and an
// already-paid payment at the same amount reports true so the retry looks successful
// rather than conflicted. Callers must pass the existing CreatedAt so a top-up does not
// reset it.
func (ms *MongoStorage) SetProcessPaymentPaidByWallet(payment *ProcessPayment) (bool, error) {
	if payment == nil || payment.ProcessID == bson.NilObjectID ||
		(payment.OrgAddress.Cmp(common.Address{}) == 0) || payment.AmountCents <= 0 {
		return false, ErrInvalidData
	}
	now := time.Now()
	payment.Status = ProcessPaymentPaid
	payment.CheckoutSessionID = ""
	payment.PaidAt = now
	if payment.CreatedAt.IsZero() {
		payment.CreatedAt = now
	}
	payment.UpdatedAt = now

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id": payment.ProcessID,
		"$or": bson.A{
			bson.M{"status": bson.M{"$in": bson.A{ProcessPaymentPending, ProcessPaymentFailed}}},
			// a top-up of this same wallet payment: only upwards, and only while no
			// checkout session owns the record
			bson.M{
				"status": ProcessPaymentPaid,
				// absent (the field is omitempty, so a wallet payment has none) or empty
				"checkoutSessionId": bson.M{"$in": bson.A{"", nil}},
				"amountCents":       bson.M{"$lt": payment.AmountCents},
			},
		},
	}
	res, err := ms.processPayments.ReplaceOne(ctx, filter, payment, options.Replace().SetUpsert(true))
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			existing, gerr := ms.ProcessPayment(payment.ProcessID)
			if gerr != nil {
				return false, fmt.Errorf("failed to resolve conflicting process payment: %w", gerr)
			}
			return existing.Status == ProcessPaymentPaid, nil
		}
		return false, fmt.Errorf("failed to set process payment paid by wallet: %w", err)
	}
	return res.MatchedCount == 1 || res.UpsertedCount == 1, nil
}

// SetProcessPaymentFree records a €0 paid payment for a process that was published free, so
// its census has an envelope to grow against like any paid process: growth past the free
// price is refused with what it costs and bought through the same census checkout. Insert
// only — a process that already has a payment keeps it — and reports whether it inserted.
func (ms *MongoStorage) SetProcessPaymentFree(processID bson.ObjectID, orgAddress common.Address) (bool, error) {
	if processID == bson.NilObjectID || (orgAddress.Cmp(common.Address{}) == 0) {
		return false, ErrInvalidData
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.processPayments.InsertOne(ctx, &ProcessPayment{
		ProcessID:  processID,
		OrgAddress: orgAddress,
		Status:     ProcessPaymentPaid,
		Currency:   "eur",
		PaidAt:     now,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to set free process payment: %w", err)
	}
	return true, nil
}

// DeleteProcessPayment removes the payment state of a process. Used when a draft is
// deleted; callers must refuse to delete drafts with a processing or paid payment first.
func (ms *MongoStorage) DeleteProcessPayment(processID bson.ObjectID) error {
	if processID == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processPayments.DeleteOne(ctx, bson.M{"_id": processID}); err != nil {
		return fmt.Errorf("failed to delete process payment: %w", err)
	}
	return nil
}

// transitionProcessPayment applies one CAS state transition: the payment must hold the
// given checkout session and be in one of fromStatuses. Reports whether this call won the
// transition.
func (ms *MongoStorage) transitionProcessPayment(
	processID bson.ObjectID, sessionID string, fromStatuses bson.A, set bson.M,
) (bool, error) {
	if processID == bson.NilObjectID || sessionID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":               processID,
		"checkoutSessionId": sessionID,
		"status":            bson.M{"$in": fromStatuses},
	}
	set["updatedAt"] = time.Now()
	res, err := ms.processPayments.UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return false, fmt.Errorf("failed to transition process payment: %w", err)
	}
	return res.MatchedCount == 1, nil
}
