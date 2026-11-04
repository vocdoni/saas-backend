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

// SetProcessPaymentPending stores a new open checkout session. It applies only if the stored
// session is still replacesSessionID (the one the caller expired; empty if it saw none), so two
// concurrent checkouts cannot both win; reports false otherwise. What the process paid is not on
// this row (ProcessLedgerState): the caller refuses a paid or refunded draft before.
//
// Only a first session (replacesSessionID empty) inserts the row. A replacement never does: if
// the row it replaces is gone — its session's expiry dropped it and released the draft's add-on
// claims in between — inserting would store a session with no claim behind it, so it reports
// false and the caller expires the new session.
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
	// a row a wallet payment wrote may carry no session (stored absent): "" matches it
	var replaces any = replacesSessionID
	if replacesSessionID == "" {
		replaces = bson.M{"$in": bson.A{"", nil}}
	}
	filter := bson.M{"_id": payment.ProcessID, "checkoutSessionId": replaces}
	res, err := ms.processPayments.ReplaceOne(ctx, filter, payment, options.Replace().SetUpsert(replacesSessionID == ""))
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

// SetProcessPaymentTopUpRefusal records why the top-up webhook did not publish the process whose
// payment sessionID still backs (pending, paid by it, or reverted after it); it reports whether
// it found that payment.
func (ms *MongoStorage) SetProcessPaymentTopUpRefusal(
	processID bson.ObjectID, sessionID string, refusal TopUpRefusal,
) (bool, error) {
	return ms.transitionProcessPayment(processID, sessionID, processPaymentTransition{
		from: bson.A{ProcessPaymentPending, ProcessPaymentPaid, ProcessPaymentFailed},
		set:  bson.M{"topUpRefusal": refusal},
	})
}

// SetProcessPaymentPublishJob records jobID as the latest publish job of processID's payment. A
// process with no payment (a free one) records nothing.
func (ms *MongoStorage) SetProcessPaymentPublishJob(processID bson.ObjectID, jobID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processPayments.UpdateOne(ctx, bson.M{"_id": processID},
		bson.M{"$set": bson.M{"publishJobId": jobID}}); err != nil {
		return fmt.Errorf("failed to record publish job: %w", err)
	}
	return nil
}

// DeleteProcessPaymentCheckout drops the pending payment holding sessionID: its checkout expired,
// failed, or was cancelled. A compare-and-set on the session, so a newer checkout of the same
// draft is left alone; reports whether it dropped it.
func (ms *MongoStorage) DeleteProcessPaymentCheckout(processID bson.ObjectID, sessionID string) (bool, error) {
	if processID == bson.NilObjectID || sessionID == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processPayments.DeleteOne(ctx,
		bson.M{"_id": processID, "status": ProcessPaymentPending, "checkoutSessionId": sessionID})
	if err != nil {
		return false, fmt.Errorf("failed to delete process payment checkout: %w", err)
	}
	return res.DeletedCount == 1, nil
}

// SetProcessPaymentCacheParams is the display cache SetProcessPaymentCache writes.
type SetProcessPaymentCacheParams struct {
	ProcessID  bson.ObjectID
	OrgAddress common.Address
	// Status is paid, failed or refunded.
	Status      ProcessPaymentStatus
	AmountCents pricing.Cents
}

// SetProcessPaymentCache refreshes the display cache of what the process paid (status paid,
// failed or refunded, and amountCents) after a money path moved it, creating the row when there is
// none. It never touches an open checkout (pending, not consumed): that row is the checkout's
// state machine. Paid drops the consumed top-up and any refusal, and keeps the first paidAt.
func (ms *MongoStorage) SetProcessPaymentCache(p SetProcessPaymentCacheParams) error {
	processID, orgAddress, status, amountCents := p.ProcessID, p.OrgAddress, p.Status, p.AmountCents
	if processID == bson.NilObjectID || (orgAddress.Cmp(common.Address{}) == 0) || amountCents < 0 ||
		(status != ProcessPaymentPaid && status != ProcessPaymentFailed && status != ProcessPaymentRefunded) {
		return ErrInvalidData
	}
	now := time.Now()
	update := bson.M{
		"$set":         bson.M{"status": status, "amountCents": amountCents, "updatedAt": now},
		"$setOnInsert": bson.M{"orgAddress": orgAddress, "currency": "eur", "createdAt": now},
	}
	if status == ProcessPaymentPaid {
		update["$min"] = bson.M{"paidAt": now}
		update["$unset"] = bson.M{"consumed": "", "topUpCents": "", "topUpRefusal": ""}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "$nor": bson.A{bson.M{"status": ProcessPaymentPending, "consumed": bson.M{"$ne": true}}}},
		update, options.UpdateOne().SetUpsert(true))
	// an open checkout is excluded, and the upsert then collides on _id: left as it is
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("failed to refresh process payment: %w", err)
	}
	return nil
}

// SetProcessPaymentBase records, once, what a published process paid outside the wallet
// (ProcessPayment.BaseCents): 0 when it was published with nothing left to pay from the wallet,
// the price of its census at the time for a process published before pay-per-process
// (grandfathered: what it already holds stays free). It gives the census an envelope to grow
// against like any paid process. A row with a base keeps it; reports whether this call wrote it.
// A process with no row, a failed one or a consumed checkout also gets the paid display cache at
// baseCents; a row a wallet payment refreshed keeps its own.
func (ms *MongoStorage) SetProcessPaymentBase(
	processID bson.ObjectID, orgAddress common.Address, baseCents pricing.Cents,
) (bool, error) {
	if processID == bson.NilObjectID || (orgAddress.Cmp(common.Address{}) == 0) || baseCents < 0 {
		return false, ErrInvalidData
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	noBase := bson.M{"$exists": false}
	res, err := ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "baseCents": noBase, "$or": bson.A{
			bson.M{"status": ProcessPaymentFailed},
			bson.M{"status": ProcessPaymentPending, "consumed": true},
		}},
		bson.M{
			"$set": bson.M{
				"baseCents": baseCents, "status": ProcessPaymentPaid, "amountCents": baseCents,
				"paidAt": now, "updatedAt": now,
			},
			"$unset": bson.M{"consumed": "", "topUpCents": "", "topUpRefusal": ""},
		})
	if err != nil {
		return false, fmt.Errorf("failed to set process payment base: %w", err)
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	res, err = ms.processPayments.UpdateOne(ctx,
		bson.M{"_id": processID, "baseCents": noBase},
		bson.M{
			"$set": bson.M{"baseCents": baseCents, "updatedAt": now},
			"$setOnInsert": bson.M{
				"orgAddress": orgAddress, "status": ProcessPaymentPaid, "amountCents": baseCents,
				"currency": "eur", "paidAt": now, "createdAt": now,
			},
		}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		// a row with a base is excluded, and the upsert then collides on _id
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to set process payment base: %w", err)
	}
	return res.MatchedCount == 1 || res.UpsertedCount == 1, nil
}

// DeleteProcessPayment removes a draft's payment row when the draft is deleted. What the process
// paid and got back stays in the wallet ledger, the audit trail.
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

// processMovementsMatch selects payer's payment and creditBack rows of processIDs: what
// ProcessLedgerState folds (a teardown's add-on credit is not one), on the {orgAddress, processId}
// index.
func processMovementsMatch(payer common.Address, processIDs bson.A) bson.M {
	return bson.M{
		"orgAddress":     payer,
		"processId":      bson.M{"$in": processIDs},
		"kind":           bson.M{"$in": bson.A{WalletEntryPayment, WalletEntryCreditBack}},
		"idempotencyKey": bson.M{"$not": bson.Regex{Pattern: "^" + teardownAddOnKeyPrefix}},
	}
}

// OrganizationAddOnWithholdings returns the credit-backs in payer's wallet ledger that withheld a
// once-per-organization add-on of orgAddress's processes, oldest first: each add-on kept back
// because another process relied on it, whose price stayed the organization's stamp. An
// organization teardown credits back the ones no process received. It drains the wallet first.
func (ms *MongoStorage) OrganizationAddOnWithholdings(payer, orgAddress common.Address) ([]WalletLedgerEntry, error) {
	if (payer.Cmp(common.Address{}) == 0) || (orgAddress.Cmp(common.Address{}) == 0) {
		return nil, ErrInvalidData
	}
	if err := ms.drainWallet(payer); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.walletLedger.Find(ctx, bson.M{
		"orgAddress":        payer,
		"processOrgAddress": orgAddress,
		"kind":              WalletEntryCreditBack,
		"withheldAddOns.0":  bson.M{"$exists": true},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to find add-on withholdings: %w", err)
	}
	var rows []WalletLedgerEntry
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode add-on withholdings: %w", err)
	}
	return rows, nil
}

// brandedProcessesFilter matches the organization's processes other than exceptProcessID that
// carry the once-per-organization addOn.
func brandedProcessesFilter(orgAddress common.Address, addOn OrgAddOn, exceptProcessID bson.ObjectID) bson.M {
	return bson.M{"orgAddress": orgAddress, "_id": bson.M{"$ne": exceptProcessID}, "addOns." + string(addOn): true}
}

// OrganizationAddOnDelivered reports whether a process of the organization other than
// exceptProcessID carries the once-per-organization addOn and has actually received it: it is
// published, or some of its elections are mined already. An organization teardown withholds an
// add-on from a credit-back only for such a process: every other draft goes in the same sweep and
// never gets to use the stamp.
func (ms *MongoStorage) OrganizationAddOnDelivered(
	orgAddress common.Address, addOn OrgAddOn, exceptProcessID bson.ObjectID,
) (bool, error) {
	if !addOn.IsValid() {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// a published process with the add-on relies on it whatever its payment says
	filter := brandedProcessesFilter(orgAddress, addOn, exceptProcessID)
	filter["$or"] = bson.A{bson.M{"published": true}, bson.M{"hasMinedElections": true}}
	n, err := ms.votingProcesses.CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count branded processes delivered: %w", err)
	}
	return n > 0, nil
}

// OrganizationAddOnReliedOnParams names the add-on OrganizationAddOnReliedOn asks about: AddOn of
// OrgAddress, whose processes are paid from Payer's wallet, relied on by a process other than
// ExceptProcessID.
type OrganizationAddOnReliedOnParams struct {
	Payer           common.Address
	OrgAddress      common.Address
	AddOn           OrgAddOn
	ExceptProcessID bson.ObjectID
}

// OrganizationAddOnReliedOn reports whether a process of the organization other than
// exceptProcessID carries the once-per-organization addOn and is published, being published (it
// holds the publish claim), has elections mined already, or is paid for (its rows in the payer's
// wallet ledger net a payment) or being paid for (an open checkout). Such a process got (or is getting) the add-on free
// on the strength of the organization's stamp, so the process that paid for the stamp cannot hand
// its price back when it is deleted: the add-on it bought is in use. A pending checkout counts: it
// was priced without the add-on, and its payment pins that price, so once paid it would publish
// with it unpaid. A draft being published, or resumed after some of its elections mined, counts
// before any payment of its own is written: a free one gets its payment record only once it is
// published.
//
// "Being published" is read off the publish claim, which a checkout, a checkout cancel and a draft
// delete take too: a branded sibling inside one of those requests at that instant also counts, and
// the add-on is withheld. Accepted: no money leaves the system (the stamp stays the organization's
// and its next process rides on it free), and the window is one request.
func (ms *MongoStorage) OrganizationAddOnReliedOn(p OrganizationAddOnReliedOnParams) (bool, error) {
	payer, orgAddress, addOn, exceptProcessID := p.Payer, p.OrgAddress, p.AddOn, p.ExceptProcessID
	delivered, err := ms.OrganizationAddOnDelivered(orgAddress, addOn, exceptProcessID)
	if err != nil || delivered {
		return delivered, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	branded := brandedProcessesFilter(orgAddress, addOn, exceptProcessID)
	branded["published"] = bson.M{"$ne": true}
	inFlight := brandedProcessesFilter(orgAddress, addOn, exceptProcessID)
	inFlight["published"] = bson.M{"$ne": true}
	inFlight["publishing"] = bson.M{"$exists": true}
	publishing, err := ms.votingProcesses.CountDocuments(ctx, inFlight, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count branded drafts being published: %w", err)
	}
	if publishing > 0 {
		return true, nil
	}
	// otherwise only a branded draft that is paid for (or being paid) does, so the payments
	// looked up are those of the organization's branded drafts, not of everything it published
	cursor, err := ms.votingProcesses.Find(ctx, branded, options.Find().SetProjection(bson.M{"_id": 1}))
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
	n, err := ms.processPayments.CountDocuments(ctx,
		bson.M{"_id": bson.M{"$in": draftIDs}, "status": ProcessPaymentPending}, options.Count().SetLimit(1))
	if err != nil {
		return false, fmt.Errorf("failed to count branded drafts being paid: %w", err)
	}
	if n > 0 {
		return true, nil
	}
	// paid: what its ledger nets, as ProcessLedgerState sums it, after posting any movement of
	// the payer's wallet still waiting; the drafts of one organization share its payer, whose
	// rows the {orgAddress, processId} index serves
	if err := ms.drainWallet(payer); err != nil {
		return false, err
	}
	sums, err := ms.walletLedger.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: processMovementsMatch(payer, draftIDs)}},
		{{Key: "$group", Value: bson.M{"_id": "$processId", "cents": bson.M{"$sum": bson.M{
			"$add": bson.A{"$amountCents", bson.M{"$ifNull": bson.A{"$withheldCents", 0}}},
		}}}}},
		{{Key: "$match", Value: bson.M{"cents": bson.M{"$lt": 0}}}},
		{{Key: "$limit", Value: 1}},
	})
	if err != nil {
		return false, fmt.Errorf("failed to sum paid branded drafts: %w", err)
	}
	var paid []bson.M
	if err := sums.All(ctx, &paid); err != nil {
		return false, fmt.Errorf("failed to decode paid branded drafts: %w", err)
	}
	return len(paid) > 0, nil
}
