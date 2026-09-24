package db

import (
	"fmt"
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
	c.Assert(testDB.CreditWallet(testOrgAddress, 5_000, "cs_topup_conc"), qt.IsNil)

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
	c.Assert(testDB.CreditWallet(testOrgAddress, 3_000, "cs_topup_conc_2"), qt.IsNil)
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

	c.Assert(testDB.CreditWallet(testOrgAddress, 0, "cs_zero"), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.CreditWallet(testOrgAddress, -100, "cs_neg"), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.CreditWallet(testOrgAddress, 100, ""), qt.ErrorIs, ErrInvalidData)
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
			errs[i] = testDB.CreditWallet(testOrgAddress, 1_000, fmt.Sprintf("cs_race_%d", i))
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
