package db

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// debitProcess is the ordinary case: the whole price of a process taken at once.
func debitProcess(org common.Address, cents int64, processID bson.ObjectID) error {
	return testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  org,
		ProcessID:   processID,
		AmountCents: cents,
		PriceCents:  cents,
	})
}

// topUpTestWallet is the ordinary credit: a verified top-up to the test organization.
func topUpTestWallet(cents int64, idempotencyKey string) error {
	return testDB.CreditWallet(WalletCredit{
		OrgAddress:     testOrgAddress,
		AmountCents:    cents,
		IdempotencyKey: idempotencyKey,
	})
}

func TestWalletCreditAndDebit(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	// an organization that never topped up has an implicit empty wallet
	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))

	// first top-up creates the wallet
	c.Assert(topUpTestWallet(10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(10_000))

	// replaying the same top-up (duplicate webhook) is a no-op
	c.Assert(topUpTestWallet(10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(10_000))

	// a debit within the balance succeeds
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(6_000))

	// retrying the same process keeps the original debit and still succeeds
	c.Assert(debitProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(6_000))

	// the census grew between two publish attempts: the retry is priced higher, so it
	// tops the wallet down by the difference instead of riding on the first debit
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  testOrgAddress,
		ProcessID:   processID,
		AmountCents: 1_000,
		PriceCents:  5_000,
	}), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(5_000))

	// and replaying that top-up changes nothing either
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  testOrgAddress,
		ProcessID:   processID,
		AmountCents: 1_000,
		PriceCents:  5_000,
	}), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(5_000))

	// a debit beyond the balance is refused atomically and changes nothing
	err = debitProcess(testOrgAddress, 7_000, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(5_000))

	// the ledger holds exactly one row per applied operation, newest first — including
	// both legs of the topped-up process, so the two debits are auditable separately
	total, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3))
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, WalletEntryDebit)
	c.Assert(entries[0].AmountCents, qt.Equals, int64(-1_000))
	c.Assert(entries[0].ProcessID, qt.Equals, processID)
	c.Assert(entries[1].Kind, qt.Equals, WalletEntryDebit)
	c.Assert(entries[1].AmountCents, qt.Equals, int64(-4_000))
	c.Assert(entries[1].ProcessID, qt.Equals, processID)
	c.Assert(entries[2].Kind, qt.Equals, WalletEntryTopUp)
	c.Assert(entries[2].AmountCents, qt.Equals, int64(10_000))

	// wallets are per organization
	other, err := testDB.Wallet(testAnotherOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(other.BalanceCents, qt.Equals, int64(0))
}

// TestWalletDebitConcurrency proves the no-double-spend property: with a balance that
// covers exactly one process, N concurrent debits for distinct processes let exactly one
// through; and N concurrent debits for the same process debit at most once in total.
func TestWalletDebitConcurrency(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const workers = 16
	c.Assert(topUpTestWallet(5_000, "cs_topup_conc"), qt.IsNil)

	// distinct processes racing for a balance that covers only one
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			results <- debitProcess(testOrgAddress, 5_000, bson.NewObjectID())
		})
	}
	wg.Wait()
	close(results)
	var succeeded, insufficient int
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)
		insufficient++
	}
	c.Assert(succeeded, qt.Equals, 1)
	c.Assert(insufficient, qt.Equals, workers-1)

	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))

	// the same process racing against itself must debit at most once
	c.Assert(topUpTestWallet(3_000, "cs_topup_conc_2"), qt.IsNil)
	processID := bson.NewObjectID()
	sameResults := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			sameResults <- debitProcess(testOrgAddress, 1_000, processID)
		})
	}
	wg.Wait()
	close(sameResults)
	for err := range sameResults {
		c.Assert(err, qt.IsNil)
	}
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(2_000))
}

func TestWalletInvalidInputs(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(topUpTestWallet(0, "cs_zero"), qt.ErrorIs, ErrInvalidData)
	c.Assert(topUpTestWallet(-100, "cs_neg"), qt.ErrorIs, ErrInvalidData)
	c.Assert(topUpTestWallet(100, ""), qt.ErrorIs, ErrInvalidData)
	c.Assert(debitProcess(testOrgAddress, 0, bson.NewObjectID()), qt.ErrorIs, ErrInvalidData)
	c.Assert(debitProcess(testOrgAddress, 100, bson.NilObjectID), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  testOrgAddress,
		ProcessID:   bson.NewObjectID(),
		AmountCents: 500,
		PriceCents:  100, // a delta larger than the price it tops up to
	}), qt.ErrorIs, ErrInvalidData)
}

// TestWalletConcurrentFirstCredits: top-ups that race the creation of the wallet must all
// land. Each one misses the filter on a wallet that does not exist yet and upserts, so all
// but the first collide on _id — a collision that means "someone else created it", not
// "already credited". Reading it as the latter drops that top-up's money while still
// writing its ledger row.
func TestWalletConcurrentFirstCredits(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const credits = 12
	var wg sync.WaitGroup
	errs := make([]error, credits)
	for i := range credits {
		wg.Go(func() {
			errs[i] = topUpTestWallet(1_000, fmt.Sprintf("cs_race_%d", i))
		})
	}
	wg.Wait()
	for _, err := range errs {
		c.Assert(err, qt.IsNil)
	}

	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(credits*1_000))

	// the balance and the audit trail agree
	total, _, err := testDB.WalletLedger(testOrgAddress, 1, credits)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(credits))
}

// TestWalletAppliedKeysBounded pins the two halves of the idempotency guard: the in-document
// key list stays bounded (an unbounded one eventually hits the 16 MB limit and freezes the
// wallet), and a replay whose key has aged out of it is still caught — by the ledger, which
// is the permanent record.
func TestWalletAppliedKeysBounded(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(topUpTestWallet(1_000, "cs_oldest"), qt.IsNil)

	// fill the guard window in one write, standing in for the ~5000 operations it takes
	pad := make(bson.A, 0, walletAppliedKeysWindow)
	for i := range walletAppliedKeysWindow {
		pad = append(pad, fmt.Sprintf("cs_pad_%d", i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := testDB.wallets.UpdateOne(ctx, bson.M{"_id": testOrgAddress},
		bson.M{"$push": bson.M{"appliedKeys": bson.M{"$each": pad}}})
	c.Assert(err, qt.IsNil)

	// the next credit applies and trims the window back to its ceiling, evicting cs_oldest
	c.Assert(topUpTestWallet(500, "cs_newest"), qt.IsNil)
	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.AppliedKeys, qt.HasLen, walletAppliedKeysWindow)
	c.Assert(slices.Contains(wallet.AppliedKeys, "cs_oldest"), qt.IsFalse)
	c.Assert(slices.Contains(wallet.AppliedKeys, "cs_newest"), qt.IsTrue)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(1_500))

	// replaying the evicted top-up must not credit it twice
	c.Assert(topUpTestWallet(1_000, "cs_oldest"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(1_500))

	// same for a debit: applied once, and a replay after its key aged out is a no-op
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 400, processID), qt.IsNil)
	_, err = testDB.wallets.UpdateOne(ctx, bson.M{"_id": testOrgAddress},
		bson.M{"$set": bson.M{"appliedKeys": bson.A{}}})
	c.Assert(err, qt.IsNil)
	c.Assert(debitProcess(testOrgAddress, 400, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(1_100))

	// one ledger row per applied operation, replays included
	total, _, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3)) // two top-ups + one debit
}
