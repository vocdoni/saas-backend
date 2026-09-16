package db

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrInsufficientWalletBalance is returned when a wallet debit would drive the balance
// negative. The debit is refused atomically; the wallet is left untouched.
var ErrInsufficientWalletBalance = fmt.Errorf("insufficient wallet balance")

// The wallet is money, so credits and debits are single conditional writes on the wallet
// document — never a read-check-then-write under keysLock, which two API replicas can both
// pass. The balance condition and the idempotency condition live in the same filter, so
// each operation applies at most once across restarts, replicas and webhook replays.
// The ledger insert that follows is the audit trail, deduplicated by a unique index on
// its idempotency key; the wallet document stays authoritative for the balance.

// Wallet returns an organization's prepaid wallet. An organization that never received a
// top-up has an implicit empty wallet, not an error.
func (ms *MongoStorage) Wallet(orgAddress common.Address) (*Wallet, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	wallet := &Wallet{}
	if err := ms.wallets.FindOne(ctx, bson.M{"_id": orgAddress}).Decode(wallet); err != nil {
		if err == mongo.ErrNoDocuments {
			return &Wallet{OrgAddress: orgAddress}, nil
		}
		return nil, fmt.Errorf("failed to get wallet: %w", err)
	}
	return wallet, nil
}

// CreditWallet adds a verified top-up to the wallet, creating it on first use. The
// idempotency key (the checkout session id) makes replayed webhooks no-ops: a key already
// applied leaves the balance untouched and still reports success.
func (ms *MongoStorage) CreditWallet(orgAddress common.Address, amountCents int64, idempotencyKey string) error {
	if (orgAddress.Cmp(common.Address{}) == 0) || amountCents <= 0 || idempotencyKey == "" {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": orgAddress, "appliedKeys": bson.M{"$ne": idempotencyKey}}
	update := bson.M{
		"$inc":  bson.M{"balanceCents": amountCents},
		"$push": bson.M{"appliedKeys": idempotencyKey},
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	_, err := ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		// an existing wallet whose appliedKeys already hold the key fails the filter, so
		// the upsert attempts an insert that collides on _id: that duplicate key means
		// "already credited", which is success
		return fmt.Errorf("failed to credit wallet: %w", err)
	}
	// always attempt the audit row so a crash between the balance write and the ledger
	// insert heals on the webhook retry; its unique index absorbs the duplicate
	return ms.appendWalletLedger(&WalletLedgerEntry{
		OrgAddress:     orgAddress,
		AmountCents:    amountCents,
		Kind:           WalletEntryTopUp,
		IdempotencyKey: idempotencyKey,
	})
}

// DebitWalletForProcess debits the price of a managed-organization process from its
// integrator's wallet, atomically and at most once per process: the process id is the
// idempotency key, so a publish retry keeps the original debit and reports success
// without charging again. Reports ErrInsufficientWalletBalance without touching the
// wallet when the balance does not cover the amount.
func (ms *MongoStorage) DebitWalletForProcess(
	orgAddress common.Address, amountCents int64, processID bson.ObjectID,
) error {
	if (orgAddress.Cmp(common.Address{}) == 0) || amountCents <= 0 || processID == bson.NilObjectID {
		return ErrInvalidData
	}
	idempotencyKey := processID.Hex()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":          orgAddress,
		"balanceCents": bson.M{"$gte": amountCents},
		"appliedKeys":  bson.M{"$ne": idempotencyKey},
	}
	update := bson.M{
		"$inc":  bson.M{"balanceCents": -amountCents},
		"$push": bson.M{"appliedKeys": idempotencyKey},
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	res, err := ms.wallets.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to debit wallet: %w", err)
	}
	if res.MatchedCount == 0 {
		// either the balance is short or this process was already debited — read the
		// wallet to tell them apart
		wallet, err := ms.Wallet(orgAddress)
		if err != nil {
			return err
		}
		if !slices.Contains(wallet.AppliedKeys, idempotencyKey) {
			return ErrInsufficientWalletBalance
		}
		// already debited by an earlier attempt: the retry keeps the debit
	}
	return ms.appendWalletLedger(&WalletLedgerEntry{
		OrgAddress:     orgAddress,
		AmountCents:    -amountCents,
		Kind:           WalletEntryDebit,
		IdempotencyKey: idempotencyKey,
		ProcessID:      processID,
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

// appendWalletLedger inserts one audit row; a replayed idempotency key is silently
// dropped by the unique index.
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
