package db

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrInsufficientWalletBalance is returned when a debit would make the balance negative.
var ErrInsufficientWalletBalance = fmt.Errorf("insufficient wallet balance")

// ErrManagedOrgSpendCapReached is returned when a debit would take a managed organization past
// the spend cap its integrator set.
var ErrManagedOrgSpendCapReached = fmt.Errorf("managed organization spend cap reached")

// orgSpendField is the wallet document path of a managed organization's ManagedOrgSpend. A
// lowercase hex address is a safe field name: no dots, no leading '$'.
func orgSpendField(managedOrg common.Address) string {
	return "orgSpend." + strings.ToLower(managedOrg.Hex())
}

// ManagedOrgSpend returns what the managed organization may spend and has spent from w.
func (w *Wallet) ManagedOrgSpend(managedOrg common.Address) ManagedOrgSpend {
	return w.OrgSpend[strings.ToLower(managedOrg.Hex())]
}

// withinSpendCap is the aggregation expression that holds when debiting amountCents keeps the
// managed organization at or under its cap; with no cap set it always holds.
func withinSpendCap(managedOrg common.Address, amountCents pricing.Cents) bson.M {
	field := "$" + orgSpendField(managedOrg)
	return bson.M{"$lte": bson.A{
		bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{field + ".spentCents", 0}}, amountCents}},
		bson.M{"$ifNull": bson.A{field + ".capCents", int64(math.MaxInt64)}},
	}}
}

// SetManagedOrgSpendCap sets the most the managed organization may spend from the integrator's
// wallet, or lifts the cap when capCents is nil. It creates the wallet when there is none yet, so
// a cap can be set before the first top-up. What it already spent is untouched: a cap below it
// only refuses further debits.
func (ms *MongoStorage) SetManagedOrgSpendCap(integrator, managedOrg common.Address, capCents *pricing.Cents) error {
	if (integrator.Cmp(common.Address{}) == 0) || (managedOrg.Cmp(common.Address{}) == 0) ||
		(capCents != nil && *capCents < 0) {
		return ErrInvalidData
	}
	field := orgSpendField(managedOrg) + ".capCents"
	update := bson.M{
		"$set":         bson.M{"updatedAt": time.Now()},
		"$setOnInsert": bson.M{"balanceCents": pricing.Cents(0)},
	}
	if capCents == nil {
		update["$unset"] = bson.M{field: ""}
	} else {
		update["$set"].(bson.M)[field] = *capCents
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": integrator}
	_, err := ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// a concurrent first write created the wallet: update it
		_, err = ms.wallets.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to set managed organization spend cap: %w", err)
	}
	return nil
}

// Credits and debits are single conditional writes on the wallet document (balance and
// idempotency key in one filter), so each applies at most once across replicas and replays.
// The ledger row that follows is the audit trail.

// walletAppliedKeysWindow bounds Wallet.AppliedKeys below the 16 MB document limit; older
// replays are caught by the ledger (walletKeyApplied).
// ponytail: a crash between the balance write and the ledger insert heals on retry only while
// the key is still in the window.
const walletAppliedKeysWindow = 5000

// appliedKeysPush is the $push that records an applied key and keeps the array bounded.
func appliedKeysPush(idempotencyKey string) bson.M {
	return bson.M{"appliedKeys": bson.M{
		"$each":  bson.A{idempotencyKey},
		"$slice": -walletAppliedKeysWindow,
	}}
}

// walletKeyApplied reports whether the ledger already has the key, catching replays whose
// key left Wallet.AppliedKeys.
func (ms *MongoStorage) walletKeyApplied(orgAddress common.Address, idempotencyKey string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	err := ms.walletLedger.FindOne(ctx,
		bson.M{"orgAddress": orgAddress, "idempotencyKey": idempotencyKey}).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, nil
	default:
		return false, fmt.Errorf("failed to look up wallet ledger entry: %w", err)
	}
}

// Wallet returns a payer's prepaid wallet, empty if it was never credited.
func (ms *MongoStorage) Wallet(orgAddress common.Address) (*Wallet, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	wallet := &Wallet{}
	// appliedKeys is only used in update filters
	opts := options.FindOne().SetProjection(bson.M{"appliedKeys": 0})
	if err := ms.wallets.FindOne(ctx, bson.M{"_id": orgAddress}, opts).Decode(wallet); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &Wallet{OrgAddress: orgAddress}, nil
		}
		return nil, fmt.Errorf("failed to get wallet: %w", err)
	}
	return wallet, nil
}

// WalletCredit adds money to a wallet: a top-up (Kind WalletEntryTopUp, the default; key: the checkout
// session id, with the payment intent and the VAT-included amount a refund needs) or a
// credit-back (WalletEntryCreditBack with the process, key: "refund:<process id hex>" for a
// deleted draft, "publishfail:<process id hex>:<attempt>" for a publish that failed for good).
type WalletCredit struct {
	OrgAddress        common.Address
	AmountCents       pricing.Cents
	IdempotencyKey    string
	Kind              string
	ProcessID         bson.ObjectID
	ProcessOrgAddress common.Address
	// SpentBy is the managed organization whose process the credit returns money for; its
	// spending goes down by the credit, freeing room under its cap. Zero for a top-up, and for a
	// credit-back to a payer that is the process's own organization (no cap applies).
	SpentBy         common.Address
	PaymentIntentID string
	GrossCents      pricing.Cents
}

// CreditWallet adds a credit (Kind defaults to WalletEntryTopUp), creating the wallet on first
// use. An already applied key is a successful no-op.
func (ms *MongoStorage) CreditWallet(c WalletCredit) error {
	kind := c.Kind
	if kind == "" {
		kind = WalletEntryTopUp
	}
	if c.AmountCents <= 0 || (kind != WalletEntryTopUp && kind != WalletEntryCreditBack) {
		return ErrInvalidData
	}
	return ms.applyWalletEntry(&WalletLedgerEntry{
		OrgAddress:        c.OrgAddress,
		AmountCents:       c.AmountCents,
		Kind:              kind,
		IdempotencyKey:    c.IdempotencyKey,
		ProcessID:         c.ProcessID,
		ProcessOrgAddress: c.ProcessOrgAddress,
		PaymentIntentID:   c.PaymentIntentID,
		GrossCents:        c.GrossCents,
	}, c.SpentBy)
}

// WalletRefund is money a top-up's payer got back through Stripe — a refund made in the
// dashboard, or a dispute's withdrawn funds — debited from the wallet the top-up credited.
type WalletRefund struct {
	OrgAddress  common.Address
	AmountCents pricing.Cents // net of VAT, positive
	// Key is the idempotency key: "striperefund:<refund id>" or "dispute:<dispute id>"
	Key string
}

// DebitWalletForRefund takes a refunded or disputed top-up back out of the wallet, in full even
// below zero: the money already left through Stripe, so this only records it. A negative balance
// refuses every DebitWalletForProcess until a top-up covers it. Keyed by r.Key, so a replay or a
// later update of the same refund or dispute debits nothing.
func (ms *MongoStorage) DebitWalletForRefund(r WalletRefund) error {
	if r.AmountCents <= 0 || r.Key == "" {
		return ErrInvalidData
	}
	return ms.applyWalletEntry(&WalletLedgerEntry{
		OrgAddress:     r.OrgAddress,
		AmountCents:    -r.AmountCents,
		Kind:           WalletEntryRefund,
		IdempotencyKey: r.Key,
	}, common.Address{})
}

// ReverseWalletRefund credits back a refund debit that Stripe undid afterwards — a refund that
// failed or was canceled once debited, a dispute whose funds were reinstated. It credits exactly
// the ledger row keyed debitKey, as a positive refund row keyed creditKey, so the history shows
// the debit and its reversal as two refund rows netting to zero; a replay credits nothing. It
// reports false, changing nothing, when no such debit was recorded.
func (ms *MongoStorage) ReverseWalletRefund(debitKey, creditKey string) (bool, error) {
	if debitKey == "" || creditKey == "" {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var debit WalletLedgerEntry
	err := ms.walletLedger.FindOne(ctx, bson.M{"idempotencyKey": debitKey, "kind": WalletEntryRefund}).Decode(&debit)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to look up refund debit %s: %w", debitKey, err)
	}
	return true, ms.applyWalletEntry(&WalletLedgerEntry{
		OrgAddress:     debit.OrgAddress,
		AmountCents:    -debit.AmountCents,
		Kind:           WalletEntryRefund,
		IdempotencyKey: creditKey,
	}, common.Address{})
}

// applyWalletEntry applies entry.AmountCents (signed) to the wallet with no balance check,
// creating the wallet on first use, then appends the entry. An already applied key is a no-op.
// A non-zero spentBy is the managed organization whose spending the entry moves the opposite way
// (a credit-back returns room under its cap); a refund debit passes the zero address and touches
// no cap.
func (ms *MongoStorage) applyWalletEntry(entry *WalletLedgerEntry, spentBy common.Address) error {
	orgAddress, idempotencyKey := entry.OrgAddress, entry.IdempotencyKey
	if (orgAddress.Cmp(common.Address{}) == 0) || entry.AmountCents == 0 || idempotencyKey == "" {
		return ErrInvalidData
	}
	applied, err := ms.walletKeyApplied(orgAddress, idempotencyKey)
	if err != nil {
		return err
	}
	if applied {
		return nil // already applied, by an attempt whose key may have left the window
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": orgAddress, "appliedKeys": bson.M{"$ne": idempotencyKey}}
	inc := bson.M{"balanceCents": entry.AmountCents}
	if spentBy != (common.Address{}) {
		inc[orgSpendField(spentBy)+".spentCents"] = -entry.AmountCents
	}
	update := bson.M{
		"$inc":  inc,
		"$push": appliedKeysPush(idempotencyKey),
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	_, err = ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// the wallet exists: either the key was applied already, or a concurrent first write
		// created it. Retrying without upsert applies only in the second case.
		_, err = ms.wallets.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to update wallet: %w", err)
	}
	// always write the audit row, so a crash before it heals on retry (unique index dedups)
	return ms.appendWalletLedger(entry)
}

// WalletDebit takes AmountCents now towards a process price of PriceCents; (process, attempt,
// price) is the idempotency key, so a retry at the same price charges nothing. Attempt is the
// payment's WalletAttempt: a publish after a credit-back pays again under a new key.
// ProcessOrgAddress is the organization owning the process, recorded on the ledger.
type WalletDebit struct {
	OrgAddress        common.Address
	ProcessID         bson.ObjectID
	ProcessOrgAddress common.Address
	AmountCents       pricing.Cents
	PriceCents        pricing.Cents
	Attempt           int
	// ManagedOrg is the managed organization the process belongs to: the debit counts towards
	// its spending and is refused past its cap. Zero skips the cap (a standard organization
	// paying from its own wallet). ProcessOrgAddress is set either way, for the history.
	ManagedOrg common.Address
}

// walletDebitKey is the idempotency key of a process debit. Attempt 0 keeps the "<process>:<price>"
// shape of a first debit; later attempts insert the attempt, a third segment no attempt-0 key has.
func walletDebitKey(processID bson.ObjectID, attempt int, priceCents pricing.Cents) string {
	if attempt == 0 {
		return fmt.Sprintf("%s:%d", processID.Hex(), priceCents)
	}
	return fmt.Sprintf("%s:%d:%d", processID.Hex(), attempt, priceCents)
}

// DebitWalletForProcess debits the payer's wallet at most once per (process, attempt, price), or
// returns ErrInsufficientWalletBalance or ErrManagedOrgSpendCapReached without touching it. The
// balance, the managed organization's cap and the idempotency key are one filter, so the cap holds
// under concurrent debits exactly as the balance does.
func (ms *MongoStorage) DebitWalletForProcess(d WalletDebit) error {
	if (d.OrgAddress.Cmp(common.Address{}) == 0) || d.AmountCents <= 0 ||
		d.PriceCents < d.AmountCents || d.ProcessID == bson.NilObjectID || d.Attempt < 0 {
		return ErrInvalidData
	}
	orgAddress, amountCents := d.OrgAddress, d.AmountCents
	idempotencyKey := walletDebitKey(d.ProcessID, d.Attempt, d.PriceCents)
	applied, err := ms.walletKeyApplied(orgAddress, idempotencyKey)
	if err != nil {
		return err
	}
	if applied {
		return nil // already debited at this price, possibly beyond the appliedKeys window
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":          orgAddress,
		"balanceCents": bson.M{"$gte": amountCents},
		"appliedKeys":  bson.M{"$ne": idempotencyKey},
	}
	inc := bson.M{"balanceCents": -amountCents}
	if d.ManagedOrg != (common.Address{}) {
		filter["$expr"] = withinSpendCap(d.ManagedOrg, amountCents)
		inc[orgSpendField(d.ManagedOrg)+".spentCents"] = amountCents
	}
	update := bson.M{
		"$inc":  inc,
		"$push": appliedKeysPush(idempotencyKey),
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	res, err := ms.wallets.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to debit wallet: %w", err)
	}
	if res.MatchedCount == 0 {
		// already debited, over the cap, or short balance
		n, err := ms.wallets.CountDocuments(ctx, bson.M{"_id": orgAddress, "appliedKeys": idempotencyKey})
		if err != nil {
			return fmt.Errorf("failed to check wallet debit: %w", err)
		}
		if n == 0 {
			if d.ManagedOrg != (common.Address{}) {
				over, err := ms.wallets.CountDocuments(ctx, bson.M{
					"_id": orgAddress, "$expr": bson.M{"$not": bson.A{withinSpendCap(d.ManagedOrg, amountCents)}},
				})
				if err != nil {
					return fmt.Errorf("failed to check managed organization spend cap: %w", err)
				}
				if over > 0 {
					return ErrManagedOrgSpendCapReached
				}
			}
			return ErrInsufficientWalletBalance
		}
		// already debited by an earlier attempt: the retry keeps the debit
	}
	return ms.appendWalletLedger(&WalletLedgerEntry{
		OrgAddress:        orgAddress,
		AmountCents:       -amountCents,
		Kind:              WalletEntryPayment,
		IdempotencyKey:    idempotencyKey,
		ProcessID:         d.ProcessID,
		ProcessOrgAddress: d.ProcessOrgAddress,
	})
}

// WalletTopUpByPaymentIntent returns the top-up ledger entry a Stripe payment intent paid for,
// or ErrNotFound when no wallet top-up was credited from it.
func (ms *MongoStorage) WalletTopUpByPaymentIntent(paymentIntentID string) (*WalletLedgerEntry, error) {
	if paymentIntentID == "" {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	entry := &WalletLedgerEntry{}
	err := ms.walletLedger.FindOne(ctx,
		bson.M{"paymentIntentId": paymentIntentID, "kind": WalletEntryTopUp}).Decode(entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find wallet top-up: %w", err)
	}
	return entry, nil
}

// WalletLedger returns a page of an organization's wallet ledger, newest first.
func (ms *MongoStorage) WalletLedger(
	orgAddress common.Address, page, limit int64,
) (int64, []WalletLedgerEntry, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	return paginatedDocuments[WalletLedgerEntry](ms.walletLedger, page, limit,
		// _id breaks createdAt ties: two rows written in the same millisecond keep insertion order
		bson.M{"orgAddress": orgAddress}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}))
}

// appendWalletLedger inserts one audit row; a replayed key is dropped by the unique index.
func (ms *MongoStorage) appendWalletLedger(entry *WalletLedgerEntry) error {
	entry.ID = bson.NewObjectID()
	entry.CreatedAt = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.walletLedger.InsertOne(ctx, entry); err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("failed to append wallet ledger entry: %w", err)
	}
	return nil
}
