package db

import (
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestWalletCreditAndDebit(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	// an organization that never topped up has an implicit empty wallet
	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))

	// first top-up creates the wallet
	c.Assert(testDB.CreditWallet(testOrgAddress, 10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(10_000))

	// replaying the same top-up (duplicate webhook) is a no-op
	c.Assert(testDB.CreditWallet(testOrgAddress, 10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(10_000))

	// a debit within the balance succeeds
	processID := bson.NewObjectID()
	c.Assert(testDB.DebitWalletForProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(6_000))

	// retrying the same process keeps the original debit and still succeeds
	c.Assert(testDB.DebitWalletForProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(6_000))

	// a debit beyond the balance is refused atomically and changes nothing
	err = testDB.DebitWalletForProcess(testOrgAddress, 7_000, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(6_000))

	// the ledger holds exactly one row per applied operation, newest first
	total, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(2))
	c.Assert(entries, qt.HasLen, 2)
	c.Assert(entries[0].Kind, qt.Equals, WalletEntryDebit)
	c.Assert(entries[0].AmountCents, qt.Equals, int64(-4_000))
	c.Assert(entries[0].ProcessID, qt.Equals, processID)
	c.Assert(entries[1].Kind, qt.Equals, WalletEntryTopUp)
	c.Assert(entries[1].AmountCents, qt.Equals, int64(10_000))

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
	c.Assert(testDB.CreditWallet(testOrgAddress, 5_000, "cs_topup_conc"), qt.IsNil)

	// distinct processes racing for a balance that covers only one
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			results <- testDB.DebitWalletForProcess(testOrgAddress, 5_000, bson.NewObjectID())
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
	c.Assert(testDB.CreditWallet(testOrgAddress, 3_000, "cs_topup_conc_2"), qt.IsNil)
	processID := bson.NewObjectID()
	sameResults := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			sameResults <- testDB.DebitWalletForProcess(testOrgAddress, 1_000, processID)
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

	c.Assert(testDB.CreditWallet(testOrgAddress, 0, "cs_zero"), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.CreditWallet(testOrgAddress, -100, "cs_neg"), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.CreditWallet(testOrgAddress, 100, ""), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.DebitWalletForProcess(testOrgAddress, 0, bson.NewObjectID()), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.DebitWalletForProcess(testOrgAddress, 100, bson.NilObjectID), qt.ErrorIs, ErrInvalidData)
}
