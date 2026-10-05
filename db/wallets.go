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

// ErrInsufficientWalletBalance is returned when a debit would make the balance negative.
var ErrInsufficientWalletBalance = fmt.Errorf("insufficient wallet balance")

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

// Wallet returns an organization's prepaid wallet, empty if it was never topped up.
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

// WalletCredit is a top-up (Kind defaults to WalletEntryTopUp, key: session id) or a refund
// (WalletEntryRefund with ProcessID, key: "refund:<process id hex>").
type WalletCredit struct {
	OrgAddress     common.Address
	AmountCents    pricing.Cents
	IdempotencyKey string
	Kind           string
	ProcessID      bson.ObjectID
}

// CreditWallet adds a credit, creating the wallet on first use. An already applied key is a
// successful no-op.
func (ms *MongoStorage) CreditWallet(c WalletCredit) error {
	orgAddress, amountCents, idempotencyKey := c.OrgAddress, c.AmountCents, c.IdempotencyKey
	if (orgAddress.Cmp(common.Address{}) == 0) || amountCents <= 0 || idempotencyKey == "" {
		return ErrInvalidData
	}
	kind := c.Kind
	if kind == "" {
		kind = WalletEntryTopUp
	}
	applied, err := ms.walletKeyApplied(orgAddress, idempotencyKey)
	if err != nil {
		return err
	}
	if applied {
		return nil // already credited, by an attempt whose key may have left the window
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": orgAddress, "appliedKeys": bson.M{"$ne": idempotencyKey}}
	update := bson.M{
		"$inc":  bson.M{"balanceCents": amountCents},
		"$push": appliedKeysPush(idempotencyKey),
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	_, err = ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// the wallet exists: either the key was applied already, or a concurrent first credit
		// created it. Retrying without upsert applies only in the second case.
		_, err = ms.wallets.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to credit wallet: %w", err)
	}
	// always write the audit row, so a crash before it heals on retry (unique index dedups)
	return ms.appendWalletLedger(&WalletLedgerEntry{
		OrgAddress:     orgAddress,
		AmountCents:    amountCents,
		Kind:           kind,
		IdempotencyKey: idempotencyKey,
		ProcessID:      c.ProcessID,
	})
}

// WalletDebit takes AmountCents now towards a process price of PriceCents; (process, price)
// is the idempotency key, so a retry at the same price charges nothing.
type WalletDebit struct {
	OrgAddress  common.Address
	ProcessID   bson.ObjectID
	AmountCents pricing.Cents
	PriceCents  pricing.Cents
}

// DebitWalletForProcess debits the integrator wallet at most once per (process, price), or
// returns ErrInsufficientWalletBalance without touching it.
func (ms *MongoStorage) DebitWalletForProcess(d WalletDebit) error {
	if (d.OrgAddress.Cmp(common.Address{}) == 0) || d.AmountCents <= 0 ||
		d.PriceCents < d.AmountCents || d.ProcessID == bson.NilObjectID {
		return ErrInvalidData
	}
	orgAddress, amountCents := d.OrgAddress, d.AmountCents
	idempotencyKey := fmt.Sprintf("%s:%d", d.ProcessID.Hex(), d.PriceCents)
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
	update := bson.M{
		"$inc":  bson.M{"balanceCents": -amountCents},
		"$push": appliedKeysPush(idempotencyKey),
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	res, err := ms.wallets.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to debit wallet: %w", err)
	}
	if res.MatchedCount == 0 {
		// short balance, or already debited
		n, err := ms.wallets.CountDocuments(ctx, bson.M{"_id": orgAddress, "appliedKeys": idempotencyKey})
		if err != nil {
			return fmt.Errorf("failed to check wallet debit: %w", err)
		}
		if n == 0 {
			return ErrInsufficientWalletBalance
		}
		// already debited by an earlier attempt: the retry keeps the debit
	}
	return ms.appendWalletLedger(&WalletLedgerEntry{
		OrgAddress:     orgAddress,
		AmountCents:    -amountCents,
		Kind:           WalletEntryDebit,
		IdempotencyKey: idempotencyKey,
		ProcessID:      d.ProcessID,
	})
}

// WalletLedger returns a page of an organization's wallet ledger, newest first.
func (ms *MongoStorage) WalletLedger(
	orgAddress common.Address, page, limit int64,
) (int64, []WalletLedgerEntry, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	return paginatedDocuments[WalletLedgerEntry](ms.walletLedger, page, limit,
		bson.M{"orgAddress": orgAddress}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
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
