package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Every transition is a conditional single-document write (the filter is the state machine);
// MatchedCount reports whether this call won it, so a replayed webhook loses the CAS instead of
// repeating side effects.

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
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get process payment: %w", err)
	}
	return payment, nil
}

// ProcessPaymentsQuery selects a page of an organization's process payments.
type ProcessPaymentsQuery struct {
	Statuses  []ProcessPaymentStatus
	Ascending bool // by creation time; newest first when false
	Page      int64
	Limit     int64
}

// OrganizationProcessPayments returns a page of an organization's process payments in the
// query's statuses, ordered by creation time.
//
// ponytail: served by the orgAddress index with an in-memory sort; add an
// {orgAddress, createdAt} index if an organization ever holds enough payments to matter.
func (ms *MongoStorage) OrganizationProcessPayments(
	orgAddress common.Address, query ProcessPaymentsQuery,
) (int64, []ProcessPayment, error) {
	if orgAddress.Cmp(common.Address{}) == 0 || len(query.Statuses) == 0 {
		return 0, nil, ErrInvalidData
	}
	order := -1
	if query.Ascending {
		order = 1
	}
	return paginatedDocuments[ProcessPayment](ms.processPayments, query.Page, query.Limit,
		bson.M{"orgAddress": orgAddress, "status": bson.M{"$in": query.Statuses}},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: order}, {Key: "_id", Value: order}}))
}

// SetProcessPaymentPending stores a new open checkout session. It applies only while there is
// no payment or a pending/failed one, and only if the stored session is still replacesSessionID
// (the one the caller expired; empty if it saw none), so two concurrent checkouts cannot both
// win. Reports false otherwise.
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
		// an excluded document makes the upsert collide on _id: that is the refusal
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to set process payment pending: %w", err)
	}
	return res.MatchedCount == 1 || res.UpsertedCount == 1, nil
}

// MarkProcessPaymentProcessing moves the pending payment holding sessionID to processing
// (a delayed payment method).
func (ms *MongoStorage) MarkProcessPaymentProcessing(processID bson.ObjectID, sessionID string) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID, processPaymentTransition{
		from: bson.A{ProcessPaymentPending},
		set:  bson.M{"status": ProcessPaymentProcessing},
	})
}

// MarkProcessPaymentPaid moves a pending or processing payment holding sessionID to paid. A
// replay reports false, so callers can skip side effects. Empty charge fields are not written.
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
	return ms.transitionProcessPayment(processID, sessionID, processPaymentTransition{
		from: bson.A{ProcessPaymentPending, ProcessPaymentProcessing},
		set:  set,
	})
}

// MarkProcessPaymentFailed moves a pending or processing payment holding sessionID to failed,
// making the process payable again.
func (ms *MongoStorage) MarkProcessPaymentFailed(processID bson.ObjectID, sessionID string) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID, processPaymentTransition{
		from: bson.A{ProcessPaymentPending, ProcessPaymentProcessing},
		set:  bson.M{"status": ProcessPaymentFailed},
	})
}

// SetProcessPaymentPaidByWallet upserts a wallet-paid payment. It never touches a card-owned
// payment, may raise a wallet payment's amount but never lower it, and reports true for an
// already-paid retry. Callers pass the existing CreatedAt and PaidAt to keep them.
func (ms *MongoStorage) SetProcessPaymentPaidByWallet(payment *ProcessPayment) (bool, error) {
	if payment == nil || payment.ProcessID == bson.NilObjectID ||
		(payment.OrgAddress.Cmp(common.Address{}) == 0) || payment.AmountCents <= 0 {
		return false, ErrInvalidData
	}
	now := time.Now()
	payment.Status = ProcessPaymentPaid
	payment.CheckoutSessionID = ""
	if payment.PaidAt.IsZero() {
		payment.PaidAt = now
	}
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
			// a top-up of this wallet payment: only upwards, never a card-owned one
			bson.M{
				"status": ProcessPaymentPaid,
				// absent or empty
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

// SetProcessPaymentEnvelope records a paid payment of amountCents that no checkout collected,
// so a published process's census has an envelope to grow against like any paid process:
// growth past that price is refused with what it costs and bought through the same census
// checkout. It is €0 for a process published free, and the price of its census at the time for
// a process published before pay-per-process (grandfathered: what it already holds stays free).
// A process that already has a payment keeps it, except a failed one: its checkout is over
// (expired, cancelled or declined) and can no longer be paid, so it is replaced — leaving it would
// give the published census no envelope at all. Reports whether it wrote.
func (ms *MongoStorage) SetProcessPaymentEnvelope(
	processID bson.ObjectID, orgAddress common.Address, amountCents int64,
) (bool, error) {
	if processID == bson.NilObjectID || (orgAddress.Cmp(common.Address{}) == 0) {
		return false, ErrInvalidData
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// with upsert, a payment the filter excludes collides on _id: that duplicate key is the refusal
	res, err := ms.processPayments.ReplaceOne(ctx,
		bson.M{"_id": processID, "status": ProcessPaymentFailed},
		&ProcessPayment{
			ProcessID:   processID,
			OrgAddress:  orgAddress,
			Status:      ProcessPaymentPaid,
			AmountCents: amountCents,
			Currency:    "eur",
			PaidAt:      now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}, options.Replace().SetUpsert(true))
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to set process payment envelope: %w", err)
	}
	return res.MatchedCount == 1 || res.UpsertedCount == 1, nil
}

// DeleteProcessPayment removes a pending or failed payment when its draft is deleted. It
// reports false for any other status, which may have moved since the caller read it.
func (ms *MongoStorage) DeleteProcessPayment(processID bson.ObjectID) (bool, error) {
	if processID == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.DeleteOne(ctx, bson.M{
		"_id":    processID,
		"status": bson.M{"$in": bson.A{ProcessPaymentPending, ProcessPaymentFailed}},
	})
	if err != nil {
		return false, fmt.Errorf("failed to delete process payment: %w", err)
	}
	return res.DeletedCount == 1, nil
}

// processPaymentTransition is one step of the payment state machine: the statuses a payment
// may move from, and the fields it gets on the way.
type processPaymentTransition struct {
	from bson.A
	set  bson.M
}

// transitionProcessPayment applies one CAS transition and reports whether this call won it.
func (ms *MongoStorage) transitionProcessPayment(
	processID bson.ObjectID, sessionID string, t processPaymentTransition,
) (bool, error) {
	if processID == bson.NilObjectID || sessionID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":               processID,
		"checkoutSessionId": sessionID,
		"status":            bson.M{"$in": t.from},
	}
	t.set["updatedAt"] = time.Now()
	res, err := ms.processPayments.UpdateOne(ctx, filter, bson.M{"$set": t.set})
	if err != nil {
		return false, fmt.Errorf("failed to transition process payment: %w", err)
	}
	return res.MatchedCount == 1, nil
}
