package db

import (
	"context"
	"errors"
	"fmt"
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

// PlanProcessPaymentRefund fixes what a delete refunds before any money moves: withheldCents,
// the net amount kept back. A compare-and-set on the paid envelope the caller read, and set
// once: it reports false when the payment is no longer paid at amountCents or already has a
// plan, and nothing has moved then. A retry reads the stored plan instead of calling this.
func (ms *MongoStorage) PlanProcessPaymentRefund(
	processID bson.ObjectID, amountCents, withheldCents pricing.Cents,
) (bool, error) {
	if processID == bson.NilObjectID || withheldCents < 0 {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{
			"_id": processID, "status": ProcessPaymentPaid, "amountCents": amountCents,
			"refundWithheldCents": bson.M{"$exists": false},
		},
		bson.M{"$set": bson.M{"refundWithheldCents": withheldCents, "updatedAt": time.Now()}})
	if err != nil {
		return false, fmt.Errorf("failed to plan process payment refund: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// DropProcessPaymentRefundPlan undoes PlanProcessPaymentRefund for a refund that never happened
// and never will (Stripe refused it for good), so the draft is not stranded behind a plan no
// retry can carry out. A compare-and-set on the plan the caller fixed: it reports false when the
// payment moved since, and then leaves it alone.
func (ms *MongoStorage) DropProcessPaymentRefundPlan(
	processID bson.ObjectID, amountCents, withheldCents pricing.Cents,
) (bool, error) {
	if processID == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{
			"_id": processID, "status": ProcessPaymentPaid, "amountCents": amountCents,
			"refundWithheldCents": withheldCents,
		},
		bson.M{"$unset": bson.M{"refundWithheldCents": ""}, "$set": bson.M{"updatedAt": time.Now()}})
	if err != nil {
		return false, fmt.Errorf("failed to drop process payment refund plan: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// MarkProcessPaymentRefunded records that a paid payment's money was returned. Only a paid
// payment transitions, so a retry of a delete that already refunded reports false instead of
// refunding twice — the caller issues the refund under an idempotency key and this CAS is
// what keeps the record honest about it. The document is kept after the draft is deleted: it
// is the only trace that money moved in and back out.
//
// amountCents is the envelope the caller refunded. A refund plan (PlanProcessPaymentRefund)
// already keeps it from growing, so the compare-and-set is only a guard: a payment that moved
// anyway reports false rather than being recorded as fully refunded.
func (ms *MongoStorage) MarkProcessPaymentRefunded(
	processID bson.ObjectID, refundID string, amountCents pricing.Cents,
) (bool, error) {
	if processID == bson.NilObjectID || refundID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "status": ProcessPaymentPaid, "amountCents": amountCents},
		bson.M{"$set": bson.M{
			"status": ProcessPaymentRefunded, "refundId": refundID, "updatedAt": time.Now(),
		}})
	if err != nil {
		return false, fmt.Errorf("failed to mark process payment refunded: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// MarkProcessPaymentRefundFailed puts a payment Stripe could not actually refund back to
// paid, keeping refundId so the failed attempt stays visible. The draft is already deleted at
// this point, so this is a manual-reconciliation signal, not a state anything recovers from
// on its own — callers log it at error level.
func (ms *MongoStorage) MarkProcessPaymentRefundFailed(processID bson.ObjectID, refundID string) (bool, error) {
	if processID == bson.NilObjectID || refundID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "status": ProcessPaymentRefunded, "refundId": refundID},
		bson.M{"$set": bson.M{"status": ProcessPaymentPaid, "updatedAt": time.Now()}})
	if err != nil {
		return false, fmt.Errorf("failed to revert failed process payment refund: %w", err)
	}
	return res.MatchedCount == 1, nil
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
			// only a wallet payment covering this amount is a retry that already paid; a card
			// payment, or a smaller one, would read as recorded to a caller that just debited,
			// and a planned payment is being refunded, so it is not paid for this caller either
			return existing.Status == ProcessPaymentPaid && existing.CheckoutSessionID == "" &&
				existing.AmountCents >= payment.AmountCents && existing.RefundWithheldCents == nil, nil
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

// processTopUpSessionsWindow bounds ProcessPayment.TopUpSessions the way
// walletAppliedKeysWindow bounds a wallet's applied keys: an unbounded $push eventually
// reaches the 16 MB document limit and a payment that can no longer be updated can never be
// raised again. A process bought this many census top-ups is already far outside
// self-service pricing, so the window is small.
// ponytail: a fixed window — the amountCents compare-and-set is the real replay guard, this
// list only stops a replay of the *same* session from the *same* base.
const processTopUpSessionsWindow = 50

// ProcessTopUp is one census top-up to apply to a paid process: the session that bought it
// was priced as ToCents minus FromCents, the envelope it was opened against.
type ProcessTopUp struct {
	ProcessID       bson.ObjectID
	FromCents       pricing.Cents
	ToCents         pricing.Cents
	SessionID       string
	PaymentIntentID string
}

// RaiseProcessPaymentAmount raises a paid payment's envelope from t.FromCents to t.ToCents,
// recording the checkout session that bought the increase and, when known, the payment intent
// that paid for it, so deleting the draft can refund every charge and not only the first. A
// compare-and-set on the base the session was priced from: once the envelope has moved (a
// replay of this session, or another top-up applied first) nothing matches and it reports
// false, so a session never raises from a base it did not pay the difference from. Reports
// whether this call won the raise.
func (ms *MongoStorage) RaiseProcessPaymentAmount(t ProcessTopUp) (bool, error) {
	if t.ProcessID == bson.NilObjectID || t.SessionID == "" || t.FromCents < 0 || t.ToCents <= t.FromCents {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":           t.ProcessID,
		"status":        ProcessPaymentPaid,
		"amountCents":   t.FromCents,
		"topUpSessions": bson.M{"$ne": t.SessionID},
		// a delete fixed its refund: a top-up landing now raises nothing and is refunded on arrival
		"refundWithheldCents": bson.M{"$exists": false},
	}
	update := bson.M{
		"$set": bson.M{"amountCents": t.ToCents, "updatedAt": time.Now()},
		"$push": bson.M{"topUpSessions": bson.M{
			"$each":  bson.A{t.SessionID},
			"$slice": -processTopUpSessionsWindow,
		}},
	}
	if t.PaymentIntentID != "" {
		// never windowed: dropping an intent would leave money a delete cannot refund
		update["$push"].(bson.M)["topUpIntents"] = t.PaymentIntentID
	}
	res, err := ms.processPayments.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, fmt.Errorf("failed to raise process payment amount: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// OrganizationBrandingReliedOn reports whether a process of the organization other than
// exceptProcessID carries the branding add-on and is published, paid for, or being paid for. Such
// a process got (or is getting) its branding free on the strength of the organization's stamp, so
// the process that paid for the stamp cannot hand the €149 back when it is deleted: the branding
// it bought is in use. A pending checkout counts: it was priced without branding, and its payment
// pins that price, so once paid it would publish branded with nobody having paid for it.
func (ms *MongoStorage) OrganizationBrandingReliedOn(
	orgAddress common.Address, exceptProcessID bson.ObjectID,
) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// a published branded process relies on it whatever its payment says
	branded := func(published any) bson.M {
		return bson.M{
			"orgAddress": orgAddress, "_id": bson.M{"$ne": exceptProcessID},
			"addOns.branding": true, "published": published,
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
		"status": bson.M{"$in": bson.A{ProcessPaymentPending, ProcessPaymentProcessing, ProcessPaymentPaid}},
	}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count paid branded drafts: %w", err)
	}
	return n > 0, nil
}
