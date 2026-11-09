package db

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// debitProcess is the ordinary case: the whole price of a process taken at once.
func debitProcess(org common.Address, cents pricing.Cents, processID bson.ObjectID) error {
	return testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  org,
		ProcessID:   processID,
		AmountCents: cents,
		PriceCents:  cents,
	})
}

// topUpTestWallet is the ordinary credit: a verified top-up to the test organization.
func topUpTestWallet(cents pricing.Cents, idempotencyKey string) error {
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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))

	// first top-up creates the wallet
	c.Assert(topUpTestWallet(10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000))

	// replaying the same top-up (duplicate webhook) is a no-op
	c.Assert(topUpTestWallet(10_000, "cs_topup_1"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000))

	// a debit within the balance succeeds
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(6_000))

	// retrying the same process keeps the original debit and still succeeds
	c.Assert(debitProcess(testOrgAddress, 4_000, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(6_000))

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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(5_000))

	// and replaying that top-up changes nothing either
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress:  testOrgAddress,
		ProcessID:   processID,
		AmountCents: 1_000,
		PriceCents:  5_000,
	}), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(5_000))

	// a debit beyond the balance is refused atomically and changes nothing
	err = debitProcess(testOrgAddress, 7_000, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(5_000))

	// the ledger holds exactly one row per applied operation, newest first — including
	// both legs of the topped-up process, so the two debits are auditable separately
	total, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3))
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, WalletEntryPayment)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(-1_000))
	c.Assert(entries[0].ProcessID, qt.Equals, processID)
	c.Assert(entries[1].Kind, qt.Equals, WalletEntryPayment)
	c.Assert(entries[1].AmountCents, qt.Equals, pricing.Cents(-4_000))
	c.Assert(entries[1].ProcessID, qt.Equals, processID)
	c.Assert(entries[2].Kind, qt.Equals, WalletEntryTopUp)
	c.Assert(entries[2].AmountCents, qt.Equals, pricing.Cents(10_000))

	// wallets are per organization
	other, err := testDB.Wallet(testAnotherOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(other.BalanceCents, qt.Equals, pricing.Cents(0))
}

// TestWalletDebitConcurrency: concurrent debits never overspend, and one process is debited
// at most once.
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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))

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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(2_000))
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

// TestWalletConcurrentFirstCredits: top-ups racing the wallet's creation all land; an _id
// collision is not "already credited".
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
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(credits*1_000))

	// the balance and the audit trail agree
	total, _, err := testDB.WalletLedger(testOrgAddress, 1, credits)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(credits))
}

// TestWalletAppliedKeysBounded: the key window stays bounded, and a replay whose key aged out
// is still caught by the ledger.
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
	// Wallet() projects the keys out, so read the stored document
	wallet := &Wallet{}
	c.Assert(testDB.wallets.FindOne(ctx, bson.M{"_id": testOrgAddress}).Decode(wallet), qt.IsNil)
	c.Assert(wallet.AppliedKeys, qt.HasLen, walletAppliedKeysWindow)
	c.Assert(slices.Contains(wallet.AppliedKeys, "cs_oldest"), qt.IsFalse)
	c.Assert(slices.Contains(wallet.AppliedKeys, "cs_newest"), qt.IsTrue)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_500))

	// replaying the evicted top-up must not credit it twice
	c.Assert(topUpTestWallet(1_000, "cs_oldest"), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_500))

	// same for a debit: applied once, and a replay after its key aged out is a no-op
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 400, processID), qt.IsNil)
	_, err = testDB.wallets.UpdateOne(ctx, bson.M{"_id": testOrgAddress},
		bson.M{"$set": bson.M{"appliedKeys": bson.A{}}})
	c.Assert(err, qt.IsNil)
	c.Assert(debitProcess(testOrgAddress, 400, processID), qt.IsNil)
	wallet, err = testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_100))

	// one ledger row per applied operation, replays included
	total, _, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3)) // two top-ups + one debit
}

// TestWalletLedgerKeysPerWallet: idempotency keys are per wallet. Two organizations applying the
// same key each get the credit and their ledger row (the top-up a refund looks up), while a
// replay within one wallet still applies nothing; reversing a refund debit only reads the wallet
// it names.
func TestWalletLedgerKeysPerWallet(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	first, second := common.Address{0x31}, common.Address{0x32}
	credit := func(org common.Address) {
		c.Assert(testDB.CreditWallet(WalletCredit{
			OrgAddress: org, AmountCents: 1_000, IdempotencyKey: "cs_shared", PaymentIntentID: "pi_" + org.Hex(),
		}), qt.IsNil)
	}
	credit(first)
	credit(second)
	credit(second) // replay
	for _, org := range []common.Address{first, second} {
		wallet, err := testDB.Wallet(org)
		c.Assert(err, qt.IsNil)
		c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_000))
		total, entries, err := testDB.WalletLedger(org, 1, 10)
		c.Assert(err, qt.IsNil)
		c.Assert(total, qt.Equals, int64(1))
		c.Assert(entries[0].IdempotencyKey, qt.Equals, "cs_shared")
		topUp, err := testDB.WalletTopUpByPaymentIntent("pi_" + org.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(topUp.OrgAddress, qt.Equals, org)
	}

	c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: first, AmountCents: 400, Key: "striperefund:re_shared"}), qt.IsNil)
	reversed, err := testDB.ReverseWalletRefund(second, "striperefund:re_shared", "refundfail:re_shared")
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsFalse) // the second wallet was never debited
	reversed, err = testDB.ReverseWalletRefund(first, "striperefund:re_shared", "refundfail:re_shared")
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsTrue)
	wallet, err := testDB.Wallet(first)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_000))
	_, err = testDB.ReverseWalletRefund(common.Address{}, "striperefund:re_shared", "refundfail:re_shared")
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestWalletRefundDebit: a Stripe refund of a top-up is debited in full even below zero, once per
// refund id across replays, and a negative balance refuses every process debit until a top-up
// covers it.
func TestWalletRefundDebit(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: 10_000, IdempotencyKey: "cs_refunded",
		PaymentIntentID: "pi_refunded", GrossCents: 12_100,
	}), qt.IsNil)
	topUp, err := testDB.WalletTopUpByPaymentIntent("pi_refunded")
	c.Assert(err, qt.IsNil)
	c.Assert(topUp.OrgAddress, qt.Equals, testOrgAddress)
	c.Assert(topUp.AmountCents, qt.Equals, pricing.Cents(10_000))
	c.Assert(topUp.GrossCents, qt.Equals, pricing.Cents(12_100))
	_, err = testDB.WalletTopUpByPaymentIntent("pi_unknown")
	c.Assert(err, qt.ErrorIs, ErrNotFound)

	// most of it is spent on a process
	processID, processOrg := bson.NewObjectID(), common.Address{0x42}
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress: testOrgAddress, ProcessID: processID, ProcessOrgAddress: processOrg,
		AmountCents: 8_000, PriceCents: 8_000,
	}), qt.IsNil)

	// a partial refund, then the rest: each debited once, the second below zero
	refund := func(id string, cents pricing.Cents) error {
		return testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: cents, Key: id})
	}
	c.Assert(refund("re_partial", 1_500), qt.IsNil)
	c.Assert(refund("re_partial", 1_500), qt.IsNil) // replay
	c.Assert(refund("re_rest", 8_500), qt.IsNil)
	c.Assert(refund("re_rest", 8_500), qt.IsNil) // replay
	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(-8_000))

	// in debt: no process debit goes through, however small
	err = debitProcess(testOrgAddress, 100, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)
	// the next top-up pays the debt first
	c.Assert(topUpTestWallet(9_000, "cs_after_refund"), qt.IsNil)
	c.Assert(debitProcess(testOrgAddress, 1_000, bson.NewObjectID()), qt.IsNil)
	err = debitProcess(testOrgAddress, 1, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrInsufficientWalletBalance)

	// the history shows each kind, signed as applied, with the process's organization
	_, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	kinds := map[string]pricing.Cents{}
	for _, e := range entries {
		kinds[e.Kind] += e.AmountCents
		if e.ProcessID == processID {
			c.Assert(e.ProcessOrgAddress, qt.Equals, processOrg)
		}
	}
	c.Assert(kinds, qt.DeepEquals, map[string]pricing.Cents{
		WalletEntryTopUp: 19_000, WalletEntryPayment: -9_000, WalletEntryRefund: -10_000,
	})

	c.Assert(refund("", 100), qt.ErrorIs, ErrInvalidData)
	c.Assert(refund("re_zero", 0), qt.ErrorIs, ErrInvalidData)

	// a debited refund Stripe undoes is credited back once, as a refund row; one never debited
	// changes nothing
	before, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	reversed, err := testDB.ReverseWalletRefund(testOrgAddress, "re_partial", "refundfail:re_partial")
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsTrue)
	reversed, err = testDB.ReverseWalletRefund(testOrgAddress, "re_partial", "refundfail:re_partial") // replay
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsTrue)
	reversed, err = testDB.ReverseWalletRefund(testOrgAddress, "re_never", "refundfail:re_never")
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsFalse)
	after, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(after.BalanceCents, qt.Equals, before.BalanceCents+1_500)
	_, entries, err = testDB.WalletLedger(testOrgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, WalletEntryRefund)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(1_500))
}

func TestWalletNetTopUpCents(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	net := func() pricing.Cents {
		cents, err := testDB.WalletNetTopUpCents(testOrgAddress)
		c.Assert(err, qt.IsNil)
		return cents
	}
	c.Assert(net(), qt.Equals, pricing.Cents(0)) // no wallet at all

	c.Assert(topUpTestWallet(5_000, "cs_net_a"), qt.IsNil)
	c.Assert(topUpTestWallet(3_000, "cs_net_b"), qt.IsNil)
	// spending and credit-backs are not top-ups: spent down to zero, it still put money in
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 8_000, processID), qt.IsNil)
	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: 1_000, IdempotencyKey: "refund:" + processID.Hex(),
		Kind: WalletEntryCreditBack, ProcessID: processID,
	}), qt.IsNil)
	c.Assert(net(), qt.Equals, pricing.Cents(8_000))

	// a Stripe refund takes it off, its reversal puts it back
	c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: 5_000, Key: "re_net"}), qt.IsNil)
	c.Assert(net(), qt.Equals, pricing.Cents(3_000))
	reversed, err := testDB.ReverseWalletRefund(testOrgAddress, "re_net", "refundfail:re_net")
	c.Assert(err, qt.IsNil)
	c.Assert(reversed, qt.IsTrue)
	c.Assert(net(), qt.Equals, pricing.Cents(8_000))
	c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: 8_000, Key: "re_all"}), qt.IsNil)
	c.Assert(net(), qt.Equals, pricing.Cents(0))

	// another wallet's top-ups are its own
	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: common.Address{0x43}, AmountCents: 2_000, IdempotencyKey: "cs_net_other",
	}), qt.IsNil)
	c.Assert(net(), qt.Equals, pricing.Cents(0))
}

// TestWalletLedgerEntryByID: a ledger row is read by its id within its own wallet only — another
// wallet's id, an unknown id and a zero id are not found or invalid — and a row whose posting a
// crash cut short is posted and found.
func TestWalletLedgerEntryByID(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() {
		walletStepHook = nil
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})
	other := common.Address{0xaa}
	c.Assert(topUpTestWallet(10_000, "cs_byid"), qt.IsNil)
	c.Assert(testDB.CreditWallet(WalletCredit{OrgAddress: other, AmountCents: 2_000, IdempotencyKey: "cs_other"}), qt.IsNil)
	_, rows, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(rows, qt.HasLen, 1)

	entry, err := testDB.WalletLedgerEntryByID(testOrgAddress, rows[0].ID)
	c.Assert(err, qt.IsNil)
	c.Assert(entry.IdempotencyKey, qt.Equals, "cs_byid")
	c.Assert(entry.Kind, qt.Equals, WalletEntryTopUp)
	_, err = testDB.WalletLedgerEntryByID(other, rows[0].ID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	_, err = testDB.WalletLedgerEntryByID(testOrgAddress, bson.NewObjectID())
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	_, err = testDB.WalletLedgerEntryByID(testOrgAddress, bson.NilObjectID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
	_, err = testDB.WalletLedgerEntryByID(common.Address{}, rows[0].ID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)

	// a movement whose ledger insert a crash cut short is still in the wallet's unposted list
	walletStepHook = func(walletStep, string) error { return errSimulatedCrash }
	c.Assert(topUpTestWallet(1_000, "cs_unposted_byid"), qt.ErrorIs, errSimulatedCrash)
	walletStepHook = nil
	var wallet struct {
		Unposted []WalletLedgerEntry `bson:"unposted"`
	}
	c.Assert(testDB.wallets.FindOne(context.Background(), bson.M{"_id": testOrgAddress}).Decode(&wallet), qt.IsNil)
	c.Assert(wallet.Unposted, qt.HasLen, 1)
	entry, err = testDB.WalletLedgerEntryByID(testOrgAddress, wallet.Unposted[0].ID)
	c.Assert(err, qt.IsNil)
	c.Assert(entry.IdempotencyKey, qt.Equals, "cs_unposted_byid")
	assertWalletConsistent(c, testOrgAddress)
}
