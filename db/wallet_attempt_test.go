package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestWalletDebitAttemptKeys: the debit key carries the payment's wallet attempt, so a retry of
// the same attempt and price charges nothing, while a publish after a credit-back (the next
// attempt) is charged again. Attempt 0 keeps the key debits had before attempts existed.
func TestWalletDebitAttemptKeys(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(topUpTestWallet(10_000, "cs_attempt_topup"), qt.IsNil)
	processID := bson.NewObjectID()
	debit := func(attempt int) {
		c.Assert(testDB.DebitWalletForProcess(WalletDebit{
			OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 1_000, PriceCents: 1_000, Attempt: attempt,
		}), qt.IsNil)
	}
	balance := func() pricing.Cents {
		wallet, err := testDB.Wallet(testOrgAddress)
		c.Assert(err, qt.IsNil)
		return wallet.BalanceCents
	}

	debit(0)
	debit(0)
	c.Assert(balance(), qt.Equals, pricing.Cents(9_000))
	debit(1)
	debit(1)
	c.Assert(balance(), qt.Equals, pricing.Cents(8_000))

	_, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, processID.Hex()+":1:1000")
	c.Assert(entries[1].IdempotencyKey, qt.Equals, processID.Hex()+":1000")

	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 1, PriceCents: 1, Attempt: -1,
	}), qt.Equals, ErrInvalidData)
}

// TestRevertWalletProcessPayment: a credited-back wallet payment becomes failed with its attempt
// moved on, once; the next wallet payment keeps that attempt, and so does the free-publish
// envelope that may replace a failed payment.
func TestRevertWalletProcessPayment(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPaidByWallet(&ProcessPayment{
		ProcessID: processID, OrgAddress: testOrgAddress, Payer: testOrgAddress, AmountCents: 1_515, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	// a CAS on the payment the caller credited: another amount or attempt does not match
	revert := func(amount pricing.Cents, attempt int) bool {
		reverted, err := testDB.RevertWalletProcessPayment(WalletPaymentRevert{
			ProcessID: processID, AmountCents: amount, Attempt: attempt, CreditBackCents: amount,
		})
		c.Assert(err, qt.IsNil)
		return reverted
	}
	c.Assert(revert(2_000, 0), qt.IsFalse)
	c.Assert(revert(1_515, 1), qt.IsFalse)

	c.Assert(revert(1_515, 0), qt.IsTrue)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentFailed)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
	c.Assert(payment.CreditBackCents, qt.Equals, pricing.Cents(1_515))

	// replayed: already failed, nothing to revert
	c.Assert(revert(1_515, 0), qt.IsFalse)

	// the next wallet payment carries the attempt the caller read
	stored, err = testDB.SetProcessPaymentPaidByWallet(&ProcessPayment{
		ProcessID: processID, OrgAddress: testOrgAddress, Payer: testOrgAddress, AmountCents: 1_515, Currency: "eur", WalletAttempt: 1,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	c.Assert(revert(1_515, 1), qt.IsTrue)

	// a free publish replaces the failed payment with its envelope and keeps the attempt
	written, err := testDB.SetProcessPaymentEnvelope(processID, testOrgAddress, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(written, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.WalletAttempt, qt.Equals, 2)
}

// TestProcessPaymentPendingKeepsAttempt: a checkout opened on a credited-back payment, and one
// replacing that checkout, keep the attempt the next debit is keyed by.
func TestProcessPaymentPendingKeepsAttempt(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	processID := bson.NewObjectID()
	ok, err := testDB.SetProcessPaymentPaidByWallet(&ProcessPayment{
		ProcessID: processID, OrgAddress: testOrgAddress, Payer: testOrgAddress, AmountCents: 1_716, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.RevertWalletProcessPayment(WalletPaymentRevert{
		ProcessID: processID, AmountCents: 1_716, CreditBackCents: 1_716,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_after_revert"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_replacing"), "cs_after_revert")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
}
