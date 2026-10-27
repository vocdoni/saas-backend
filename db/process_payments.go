package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/pricing"
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

// ConsumeProcessPaymentTopUp marks the pending payment holding sessionID consumed: its top-up
// was credited, so the session is spent and the publish it was opened for pays from the wallet.
// A compare-and-set on the session: it reports false when the payment no longer waits on it
// (consumed already, cancelled, replaced, paid or refunded), and the caller then does nothing
// more for that session.
func (ms *MongoStorage) ConsumeProcessPaymentTopUp(processID bson.ObjectID, sessionID string) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID, processPaymentTransition{
		from:  bson.A{ProcessPaymentPending},
		extra: bson.M{"consumed": bson.M{"$ne": true}},
		set:   bson.M{"consumed": true},
	})
}

// DeleteProcessPaymentCheckout drops the pending payment holding sessionID: its checkout expired,
// failed, or was cancelled. A compare-and-set on the session, so a newer checkout of the same
// draft is left alone; reports whether it deleted.
func (ms *MongoStorage) DeleteProcessPaymentCheckout(processID bson.ObjectID, sessionID string) (bool, error) {
	if processID == bson.NilObjectID || sessionID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.DeleteOne(ctx, bson.M{
		"_id": processID, "status": ProcessPaymentPending, "checkoutSessionId": sessionID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to delete process payment checkout: %w", err)
	}
	return res.DeletedCount == 1, nil
}

// RefundWithholding is what a delete's refund keeps back: the net cents of the add-ons another
// process relies on, and which add-ons they are.
type RefundWithholding struct {
	Cents  pricing.Cents
	AddOns []OrgAddOn
}

// PlanProcessPaymentRefund fixes what a delete refunds before any money moves: the withholding,
// the net amount kept back and the add-ons it keeps. A compare-and-set on the paid envelope the
// caller read, and set once: it reports false when the payment is no longer paid at amountCents
// or already has a plan, and nothing has moved then. A retry reads the stored plan instead.
func (ms *MongoStorage) PlanProcessPaymentRefund(
	processID bson.ObjectID, amountCents pricing.Cents, withheld RefundWithholding,
) (bool, error) {
	if processID == bson.NilObjectID || withheld.Cents < 0 {
		return false, ErrInvalidData
	}
	set := bson.M{"refundWithheldCents": withheld.Cents, "updatedAt": time.Now()}
	if len(withheld.AddOns) > 0 {
		set["refundWithheldAddOns"] = withheld.AddOns
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{
			"_id": processID, "status": ProcessPaymentPaid, "amountCents": amountCents,
			"refundWithheldCents": bson.M{"$exists": false},
		},
		bson.M{"$set": set})
	if err != nil {
		return false, fmt.Errorf("failed to plan process payment refund: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// MarkProcessPaymentRefunded records that a paid payment's money was credited back. Only a paid
// payment transitions, so a retry of a delete that already credited reports false; the credit
// itself is keyed "refund:<process>", so it cannot land twice either. The document is kept after
// the draft is deleted: it is the only trace that money moved in and back out.
//
// amountCents is the envelope the caller credited back. A refund plan (PlanProcessPaymentRefund)
// already keeps it from growing, so the compare-and-set is only a guard: a payment that moved
// anyway reports false rather than being recorded as fully refunded.
func (ms *MongoStorage) MarkProcessPaymentRefunded(processID bson.ObjectID, amountCents pricing.Cents) (bool, error) {
	if processID == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "status": ProcessPaymentPaid, "amountCents": amountCents},
		bson.M{"$set": bson.M{"status": ProcessPaymentRefunded, "updatedAt": time.Now()}})
	if err != nil {
		return false, fmt.Errorf("failed to mark process payment refunded: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// SetProcessPaymentPaidByWallet upserts a wallet-paid payment over none, a consumed pending one,
// a failed one, or a smaller paid one (census headroom). It never lowers an amount, never touches
// a pending payment whose checkout is still open, nor one a delete is refunding, and reports true
// for an already-paid retry. Callers pass the existing CreatedAt, PaidAt and CheckoutSessionID to
// keep them.
func (ms *MongoStorage) SetProcessPaymentPaidByWallet(payment *ProcessPayment) (bool, error) {
	if payment == nil || payment.ProcessID == bson.NilObjectID || (payment.Payer.Cmp(common.Address{}) == 0) ||
		(payment.OrgAddress.Cmp(common.Address{}) == 0) || payment.AmountCents <= 0 {
		return false, ErrInvalidData
	}
	now := time.Now()
	payment.Status = ProcessPaymentPaid
	payment.Consumed = false
	payment.TopUpCents = 0
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
			bson.M{"status": ProcessPaymentPending, "consumed": true},
			bson.M{"status": ProcessPaymentFailed},
			// census headroom: only upwards
			bson.M{
				"status":      ProcessPaymentPaid,
				"amountCents": bson.M{"$lt": payment.AmountCents},
				// a delete fixed its refund: the envelope it returns cannot grow under it
				"refundWithheldCents": bson.M{"$exists": false},
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
			// only a paid payment covering this amount is a retry that already paid; an open
			// checkout, or a smaller payment, would read as recorded to a caller that just
			// debited, and a planned payment is being refunded, so it is not paid for it either
			return existing.Status == ProcessPaymentPaid && existing.AmountCents >= payment.AmountCents &&
				existing.RefundWithheldCents == nil, nil
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
// A process that already has a payment keeps it, except a failed one (a reverted wallet payment)
// or a consumed pending one (its top-up is already in the wallet): neither holds money for the
// process, and leaving it would give the published census no envelope at all. Reports whether it
// wrote.
func (ms *MongoStorage) SetProcessPaymentEnvelope(
	processID bson.ObjectID, orgAddress common.Address, amountCents pricing.Cents,
) (bool, error) {
	if processID == bson.NilObjectID || (orgAddress.Cmp(common.Address{}) == 0) {
		return false, ErrInvalidData
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// with upsert, a payment the filter excludes collides on _id: that duplicate key is the refusal
	res, err := ms.processPayments.ReplaceOne(ctx,
		bson.M{"_id": processID, "$or": bson.A{
			bson.M{"status": ProcessPaymentFailed},
			bson.M{"status": ProcessPaymentPending, "consumed": true},
		}},
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
	from  bson.A
	extra bson.M // more conditions on the payment, or nil
	set   bson.M
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
	maps.Copy(filter, t.extra)
	t.set["updatedAt"] = time.Now()
	res, err := ms.processPayments.UpdateOne(ctx, filter, bson.M{"$set": t.set})
	if err != nil {
		return false, fmt.Errorf("failed to transition process payment: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// OrganizationAddOnReliedOn reports whether a process of the organization other than
// exceptProcessID carries the once-per-organization addOn and is published, paid for, or being
// paid for. Such a process got (or is getting) the add-on free on the strength of the
// organization's stamp, so the process that paid for the stamp cannot hand its price back when it
// is deleted: the add-on it bought is in use. A pending checkout counts: it was priced without the
// add-on, and its payment pins that price, so once paid it would publish with it unpaid.
func (ms *MongoStorage) OrganizationAddOnReliedOn(
	orgAddress common.Address, addOn OrgAddOn, exceptProcessID bson.ObjectID,
) (bool, error) {
	if !addOn.IsValid() {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// a published process with the add-on relies on it whatever its payment says
	branded := func(published any) bson.M {
		return bson.M{
			"orgAddress": orgAddress, "_id": bson.M{"$ne": exceptProcessID},
			"addOns." + string(addOn): true, "published": published,
		}
	}
	published, err := ms.votingProcesses.CountDocuments(ctx, branded(true), options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count published branded processes: %w", err)
	}
	if published > 0 {
		return true, nil
	}
	// otherwise only a branded draft that is paid for (or being paid) does, so the payments
	// looked up are those of the organization's branded drafts, not of everything it published
	cursor, err := ms.votingProcesses.Find(ctx, branded(bson.M{"$ne": true}), options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return false, fmt.Errorf("failed to find branded drafts: %w", err)
	}
	var drafts []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cursor.All(ctx, &drafts); err != nil {
		return false, fmt.Errorf("failed to decode branded drafts: %w", err)
	}
	if len(drafts) == 0 {
		return false, nil
	}
	draftIDs := make(bson.A, 0, len(drafts))
	for _, d := range drafts {
		draftIDs = append(draftIDs, d.ID)
	}
	n, err := ms.processPayments.CountDocuments(ctx, bson.M{
		"_id":    bson.M{"$in": draftIDs},
		"status": bson.M{"$in": bson.A{ProcessPaymentPending, ProcessPaymentPaid}},
	}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count paid branded drafts: %w", err)
	}
	return n > 0, nil
}
