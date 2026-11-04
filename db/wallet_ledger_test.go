package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// errSimulatedCrash is what walletStepHook returns to cut a movement short, as a crash would.
var errSimulatedCrash = fmt.Errorf("simulated crash")

// TestWalletDebitAttemptKeys: the debit key carries the process's attempt, so a retry of the same
// attempt and price charges nothing, while a publish after a credit-back (the next attempt) is
// charged again. Attempt 0 keeps the key debits had before attempts existed, and the row records
// the attempt, the price and the add-ons.
func TestWalletDebitAttemptKeys(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(topUpTestWallet(10_000, "cs_attempt_topup"), qt.IsNil)
	processID := bson.NewObjectID()
	debit := func(attempt int) {
		c.Assert(testDB.DebitWalletForProcess(WalletDebit{
			OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 1_000, PriceCents: 1_000, Attempt: attempt,
			AddOns: []OrgAddOn{OrgAddOnBranding},
		}), qt.IsNil)
	}

	debit(0)
	debit(0)
	c.Assert(walletBalance(c, testOrgAddress), qt.Equals, pricing.Cents(9_000))
	debit(1)
	debit(1)
	c.Assert(walletBalance(c, testOrgAddress), qt.Equals, pricing.Cents(8_000))

	_, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, processID.Hex()+":1:1000")
	c.Assert(entries[0].Attempt, qt.Equals, 1)
	c.Assert(entries[0].PriceCents, qt.Equals, pricing.Cents(1_000))
	c.Assert(entries[0].AddOns, qt.DeepEquals, []OrgAddOn{OrgAddOnBranding})
	c.Assert(entries[1].IdempotencyKey, qt.Equals, processID.Hex()+":1000")

	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 1, PriceCents: 1, Attempt: -1,
	}), qt.Equals, ErrInvalidData)
}

// walletBalance is the payer's balance as Wallet() reads it.
func walletBalance(c *qt.C, org common.Address) pricing.Cents {
	wallet, err := testDB.Wallet(org)
	c.Assert(err, qt.IsNil)
	return wallet.BalanceCents
}

// rawWallet reads the stored wallet document without draining it.
func rawWallet(c *qt.C, org common.Address) *Wallet {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	wallet := &Wallet{}
	c.Assert(testDB.wallets.FindOne(ctx, bson.M{"_id": org}).Decode(wallet), qt.IsNil)
	return wallet
}

// ledgerRows reads every ledger row of org, without draining.
func ledgerRows(c *qt.C, org common.Address) []WalletLedgerEntry {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := testDB.walletLedger.Find(ctx, bson.M{"orgAddress": org})
	c.Assert(err, qt.IsNil)
	var rows []WalletLedgerEntry
	c.Assert(cursor.All(ctx, &rows), qt.IsNil)
	return rows
}

// assertWalletConsistent: once drained, the balance is the sum of the ledger and nothing waits.
func assertWalletConsistent(c *qt.C, org common.Address) {
	wallet := rawWallet(c, org)
	c.Assert(wallet.Unposted, qt.HasLen, 0)
	var sum pricing.Cents
	for _, row := range ledgerRows(c, org) {
		sum += row.AmountCents
	}
	c.Assert(wallet.BalanceCents, qt.Equals, sum)
}

// processState reads the process's ledger state (draining the wallet).
func processState(c *qt.C, payer common.Address, processID bson.ObjectID) *ProcessLedgerState {
	state, err := testDB.ProcessLedgerState(payer, processID)
	c.Assert(err, qt.IsNil)
	return state
}

// TestWalletMovementCrashMatrix: every kind of movement cut short before its ledger row (W2) or
// before it is pulled from Unposted (W3) has moved the balance and holds its record in the wallet;
// the next touch of the wallet, or the startup sweep, posts it, a retry of the movement applies
// nothing more, and the balance equals the ledger with the process's paid amount right.
func TestWalletMovementCrashMatrix(t *testing.T) {
	processID := bson.NewObjectID()
	type movement struct {
		name  string
		key   string
		apply func() error
		delta pricing.Cents
		paid  pricing.Cents // the process's paid amount once applied
	}
	setup := func(c *qt.C) {
		c.Assert(topUpTestWallet(10_000, "cs_matrix_base"), qt.IsNil)
		c.Assert(debitProcess(testOrgAddress, 1_000, processID), qt.IsNil)
		c.Assert(testDB.DebitWalletForRefund(WalletRefund{
			OrgAddress: testOrgAddress, AmountCents: 300, Key: "striperefund:re_matrix",
		}), qt.IsNil)
	}
	creditBack := func(key string, amount, withheld pricing.Cents) func() error {
		return func() error {
			return testDB.CreditWallet(WalletCredit{
				OrgAddress: testOrgAddress, AmountCents: amount, IdempotencyKey: key, Kind: WalletEntryCreditBack,
				ProcessID: processID, ProcessOrgAddress: testOrgAddress, Withheld: RefundWithholding{Cents: withheld},
			})
		}
	}
	movements := []movement{
		{name: "top-up", key: "cs_matrix", delta: 500, paid: 1_000, apply: func() error { return topUpTestWallet(500, "cs_matrix") }},
		{
			name: "process debit", key: processID.Hex() + ":1500", delta: -500, paid: 1_500,
			apply: func() error {
				return testDB.DebitWalletForProcess(WalletDebit{
					OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 500, PriceCents: 1_500,
				})
			},
		},
		{
			name: "publishfail credit-back", key: PublishFailCreditKey(processID, 0), delta: 1_000,
			apply: creditBack(PublishFailCreditKey(processID, 0), 1_000, 0),
		},
		{
			name: "all-withheld refund", key: RefundCreditKey(processID), delta: 0,
			apply: creditBack(RefundCreditKey(processID), 0, 1_000),
		},
		{
			name: "stripe refund debit", key: "striperefund:re_matrix_2", delta: -200, paid: 1_000,
			apply: func() error {
				return testDB.DebitWalletForRefund(WalletRefund{
					OrgAddress: testOrgAddress, AmountCents: 200, Key: "striperefund:re_matrix_2",
				})
			},
		},
		{
			name: "refund reversal", key: "refundfail:re_matrix", delta: 300, paid: 1_000,
			apply: func() error {
				_, err := testDB.ReverseWalletRefund(testOrgAddress, "striperefund:re_matrix", "refundfail:re_matrix")
				return err
			},
		},
	}
	for _, m := range movements {
		for _, step := range []walletStep{walletStepPost, walletStepPull} {
			for _, sweep := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/step%d/sweep=%v", m.name, step, sweep), func(t *testing.T) {
					c := qt.New(t)
					c.Cleanup(func() {
						walletStepHook = nil
						c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
					})
					setup(c)
					before := rawWallet(c, testOrgAddress).BalanceCents
					walletStepHook = func(s walletStep, key string) error {
						if s == step && key == m.key {
							return errSimulatedCrash
						}
						return nil
					}
					c.Assert(m.apply(), qt.ErrorIs, errSimulatedCrash)
					walletStepHook = nil

					// the balance moved, and the movement is recorded on the wallet itself
					crashed := rawWallet(c, testOrgAddress)
					c.Assert(crashed.BalanceCents, qt.Equals, before+m.delta)
					c.Assert(crashed.Unposted, qt.HasLen, 1)
					c.Assert(crashed.Unposted[0].IdempotencyKey, qt.Equals, m.key)

					if sweep {
						c.Assert(testDB.DrainUnpostedWalletEntries(), qt.IsNil)
					} else {
						c.Assert(walletBalance(c, testOrgAddress), qt.Equals, before+m.delta)
					}
					assertWalletConsistent(c, testOrgAddress)
					// a retry of the cut-short movement applies nothing more
					c.Assert(m.apply(), qt.IsNil)
					assertWalletConsistent(c, testOrgAddress)
					c.Assert(rawWallet(c, testOrgAddress).BalanceCents, qt.Equals, before+m.delta)
					c.Assert(processState(c, testOrgAddress, processID).PaidCents, qt.Equals, m.paid)
				})
			}
		}
	}
}

// TestWalletUnpostedSeenByReaders: a movement cut short before its row is seen by every reader,
// since each drains first: the ledger page, the process state, and a refund looking up its top-up.
func TestWalletUnpostedSeenByReaders(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() {
		walletStepHook = nil
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})
	walletStepHook = func(walletStep, string) error { return errSimulatedCrash }
	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: 1_000, IdempotencyKey: "cs_unposted", PaymentIntentID: "pi_unposted",
	}), qt.ErrorIs, errSimulatedCrash)
	walletStepHook = nil
	topUp, err := testDB.WalletTopUpByPaymentIntent("pi_unposted")
	c.Assert(err, qt.IsNil)
	c.Assert(topUp.IdempotencyKey, qt.Equals, "cs_unposted")

	processID := bson.NewObjectID()
	walletStepHook = func(walletStep, string) error { return errSimulatedCrash }
	c.Assert(debitProcess(testOrgAddress, 400, processID), qt.ErrorIs, errSimulatedCrash)
	walletStepHook = nil
	c.Assert(processState(c, testOrgAddress, processID).PaidCents, qt.Equals, pricing.Cents(400))

	walletStepHook = func(walletStep, string) error { return errSimulatedCrash }
	c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: 100, Key: "striperefund:re_x"}),
		qt.ErrorIs, errSimulatedCrash)
	walletStepHook = nil
	total, _, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(3))
	assertWalletConsistent(c, testOrgAddress)
}

// TestProcessLedgerStateFold: the fold of a process's rows — payments add, credit-backs settle
// what they returned and what they withheld, publishfail credit-backs move the attempt on and
// clear the live add-ons, a refund marks the draft delete-only, and the last credit-back owes its
// release (pinned at its time) until a payment follows it. Other processes do not count.
func TestProcessLedgerStateFold(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(topUpTestWallet(100_000, "cs_fold"), qt.IsNil)
	processID := bson.NewObjectID()
	branding := []OrgAddOn{OrgAddOnBranding}
	debit := func(attempt int, amount, price pricing.Cents, addOns []OrgAddOn) {
		c.Assert(testDB.DebitWalletForProcess(WalletDebit{
			OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: amount, PriceCents: price, Attempt: attempt,
			AddOns: addOns,
		}), qt.IsNil)
	}
	creditBack := func(key string, amount, withheld pricing.Cents, release []OrgAddOn) {
		c.Assert(testDB.CreditWallet(WalletCredit{
			OrgAddress: testOrgAddress, AmountCents: amount, IdempotencyKey: key, Kind: WalletEntryCreditBack,
			ProcessID: processID, Withheld: RefundWithholding{Cents: withheld}, ReleaseAddOns: release,
		}), qt.IsNil)
	}
	state := func() *ProcessLedgerState { return processState(c, testOrgAddress, processID) }

	_, err := testDB.ProcessLedgerState(common.Address{}, processID)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
	empty := state()
	c.Assert(empty.Payer, qt.Equals, testOrgAddress)
	c.Assert(empty.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(empty.Attempt, qt.Equals, 0)

	// payments: the publish, then headroom; the add-ons are the live attempt's
	debit(0, 15_000, 15_000, branding)
	debit(0, 2_000, 17_000, nil)
	c.Assert(debitProcess(testOrgAddress, 3_000, bson.NewObjectID()), qt.IsNil) // another process's
	s := state()
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(17_000))
	c.Assert(s.Charged(OrgAddOnBranding), qt.IsTrue)
	c.Assert(s.OwedRelease, qt.HasLen, 0)

	// a failed publish credited back with branding withheld: settled in full, attempt 1, the
	// add-ons cleared, nothing owed back since branding stays
	creditBack(PublishFailCreditKey(processID, 0), 2_100, 14_900, nil)
	s = state()
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(s.Attempt, qt.Equals, 1)
	c.Assert(s.AddOns, qt.HasLen, 0)
	c.Assert(s.OwedRelease, qt.HasLen, 0)

	// the next attempt pays with branding, fails, and is credited back in full: the release is
	// owed, pinned at the credit-back's time, until the next payment follows it
	debit(1, 15_000, 15_000, branding)
	creditBack(PublishFailCreditKey(processID, 1), 15_000, 0, branding)
	s = state()
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(s.Attempt, qt.Equals, 2)
	c.Assert(s.OwedRelease, qt.DeepEquals, branding)
	rows := ledgerRows(c, testOrgAddress)
	c.Assert(s.OwedSince.Equal(rows[len(rows)-1].CreatedAt), qt.IsTrue)
	debit(2, 1_000, 1_000, nil)
	s = state()
	c.Assert(s.OwedRelease, qt.HasLen, 0)
	c.Assert(s.CreditedBackAt.Equal(rows[len(rows)-1].CreatedAt), qt.IsTrue)
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(1_000))
	c.Assert(s.Refunded, qt.IsFalse)

	// a delete's refund: delete-only from then on
	creditBack(RefundCreditKey(processID), 1_000, 0, nil)
	s = state()
	c.Assert(s.Refunded, qt.IsTrue)
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(s.Attempt, qt.Equals, 2)
}

// TestCreditBackSettlesWithheld: a credit-back of 0 is a valid movement when it withholds
// something, so a draft whose withheld add-ons account for its whole payment still nets to 0; a
// credit of 0 that withholds nothing is refused.
func TestCreditBackSettlesWithheld(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(topUpTestWallet(1_000, "cs_withheld"), qt.IsNil)
	processID := bson.NewObjectID()
	c.Assert(debitProcess(testOrgAddress, 149, processID), qt.IsNil)
	credit := WalletCredit{
		OrgAddress: testOrgAddress, IdempotencyKey: RefundCreditKey(processID), Kind: WalletEntryCreditBack,
		ProcessID: processID,
	}
	c.Assert(testDB.CreditWallet(credit), qt.ErrorIs, ErrInvalidData)
	credit.Withheld = RefundWithholding{Cents: 149, AddOns: []OrgAddOn{OrgAddOnBranding}}
	c.Assert(testDB.CreditWallet(credit), qt.IsNil)
	c.Assert(testDB.CreditWallet(credit), qt.IsNil) // a replay
	c.Assert(walletBalance(c, testOrgAddress), qt.Equals, pricing.Cents(851))
	s := processState(c, testOrgAddress, processID)
	c.Assert(s.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(s.Refunded, qt.IsTrue)
	_, entries, err := testDB.WalletLedger(testOrgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(0))
	c.Assert(entries[0].WithheldCents, qt.Equals, pricing.Cents(149))
	assertWalletConsistent(c, testOrgAddress)
}

// ledgerSim drives one process's money as the API's three paths do, each reading what the
// process paid off the ledger (ProcessLedgerState) and moving money with the db primitives:
// charge debits price − paid at the live attempt (only from fromCents when set); reverse credits
// paid − withheld under the attempt's publishfail key, keeping a payment its withheld add-ons
// account for entirely; del credits paid − withheld (0 allowed) under the refund key.
type ledgerSim struct {
	c   *qt.C
	pid bson.ObjectID
}

func (s ledgerSim) state() *ProcessLedgerState { return processState(s.c, testOrgAddress, s.pid) }

func (s ledgerSim) charge(price pricing.Cents, fromCents *pricing.Cents) error {
	st := s.state()
	if st.Refunded || (fromCents != nil && st.PaidCents != *fromCents) {
		return ErrConflict
	}
	if st.PaidCents >= price {
		return nil
	}
	return testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress: testOrgAddress, ProcessID: s.pid, AmountCents: price - st.PaidCents, PriceCents: price,
		Attempt: st.Attempt,
	})
}

func (s ledgerSim) reverse(withheld pricing.Cents) error {
	st := s.state()
	if st.PaidCents == 0 || st.Refunded || st.PaidCents-withheld <= 0 {
		return nil
	}
	return testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: st.PaidCents - withheld, IdempotencyKey: PublishFailCreditKey(s.pid, st.Attempt),
		Kind: WalletEntryCreditBack, ProcessID: s.pid, Withheld: RefundWithholding{Cents: withheld},
	})
}

func (s ledgerSim) del(withheld pricing.Cents) error {
	st := s.state()
	if st.PaidCents == 0 {
		return nil
	}
	withheld = min(withheld, st.PaidCents)
	return testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: st.PaidCents - withheld, IdempotencyKey: RefundCreditKey(s.pid),
		Kind: WalletEntryCreditBack, ProcessID: s.pid, Withheld: RefundWithholding{Cents: withheld},
	})
}

// TestLedgerNeverBacksMoreThanPaid walks the partial-failure sequences of the review rounds over
// the ledger: whatever the order, a process is never credited back more than it paid less what was
// withheld, every debit of a deleted draft is settled by a row, and the balances are exact.
func TestLedgerNeverBacksMoreThanPaid(t *testing.T) {
	const topUp = pricing.Cents(100_000)
	run := func(name string, walk func(c *qt.C, s ledgerSim) (balance, paid pricing.Cents)) {
		t.Run(name, func(t *testing.T) {
			c := qt.New(t)
			c.Cleanup(func() {
				walletStepHook = nil
				c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
			})
			c.Assert(topUpTestWallet(topUp, "cs_inv4"), qt.IsNil)
			s := ledgerSim{c: c, pid: bson.NewObjectID()}
			balance, paid := walk(c, s)
			c.Assert(walletBalance(c, testOrgAddress), qt.Equals, balance)
			c.Assert(s.state().PaidCents, qt.Equals, paid)
			assertWalletConsistent(c, testOrgAddress)
		})
	}
	crashOnce := func(key string) {
		walletStepHook = func(step walletStep, k string) error {
			if k == key && step == walletStepPost {
				walletStepHook = nil
				return errSimulatedCrash
			}
			return nil
		}
	}

	run("retried delete after a credit-back", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(3_900, nil), qt.IsNil)
		c.Assert(s.del(0), qt.IsNil)
		c.Assert(s.del(0), qt.IsNil) // the draft delete failed; the retry credits nothing more
		return topUp, 0
	})
	run("headroom then a failed publish", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(3_900, nil), qt.IsNil)
		from := pricing.Cents(3_900)
		c.Assert(s.charge(5_000, &from), qt.IsNil)
		c.Assert(s.reverse(0), qt.IsNil)
		c.Assert(s.reverse(0), qt.IsNil) // the stale sweep runs it again
		c.Assert(s.state().Attempt, qt.Equals, 1)
		c.Assert(s.charge(3_900, nil), qt.IsNil) // the next publish pays again, at attempt 1
		return topUp - 3_900, 3_900
	})
	run("headroom racing a reversal", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(3_900, nil), qt.IsNil)
		c.Assert(s.reverse(0), qt.IsNil)
		from := pricing.Cents(3_900) // priced before the reversal: refused after it
		c.Assert(s.charge(5_000, &from), qt.ErrorIs, ErrConflict)
		return topUp, 0
	})
	run("refund before credit", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(3_900, nil), qt.IsNil)
		c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: 10_000, Key: "striperefund:re_1"}),
			qt.IsNil)
		c.Assert(s.del(0), qt.IsNil)
		return topUp - 10_000, 0
	})
	run("dispute cycles", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(3_900, nil), qt.IsNil)
		for n := 1; n <= 2; n++ {
			key := fmt.Sprintf("dispute:dp_1:%d", n)
			c.Assert(testDB.DebitWalletForRefund(WalletRefund{OrgAddress: testOrgAddress, AmountCents: 20_000, Key: key}), qt.IsNil)
			reversed, err := testDB.ReverseWalletRefund(testOrgAddress, key, fmt.Sprintf("disputereinstated:dp_1:%d", n))
			c.Assert(err, qt.IsNil)
			c.Assert(reversed, qt.IsTrue)
			c.Assert(s.reverse(0), qt.IsNil)
			c.Assert(s.charge(3_900, nil), qt.IsNil)
		}
		return topUp - 3_900, 3_900
	})
	run("all-withheld delete", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(14_900, nil), qt.IsNil)
		c.Assert(s.reverse(14_900), qt.IsNil) // a failed publish whose add-ons are all in use stays paid
		c.Assert(s.state().PaidCents, qt.Equals, pricing.Cents(14_900))
		c.Assert(s.del(14_900), qt.IsNil)
		c.Assert(s.del(14_900), qt.IsNil)
		c.Assert(s.state().Refunded, qt.IsTrue)
		return topUp - 14_900, 0
	})
	run("debit cut short, retried at a grown price", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		crashOnce(s.pid.Hex() + ":3900")
		c.Assert(s.charge(3_900, nil), qt.ErrorIs, errSimulatedCrash)
		c.Assert(s.charge(5_000, nil), qt.IsNil) // pays only the difference
		return topUp - 5_000, 5_000
	})
	run("debit cut short, then a delete", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		crashOnce(s.pid.Hex() + ":3900")
		c.Assert(s.charge(3_900, nil), qt.ErrorIs, errSimulatedCrash)
		c.Assert(s.del(0), qt.IsNil)
		c.Assert(s.charge(3_900, nil), qt.ErrorIs, ErrConflict) // refunded: delete-only
		return topUp, 0
	})
	run("withheld branding, then a republish", func(c *qt.C, s ledgerSim) (pricing.Cents, pricing.Cents) {
		c.Assert(s.charge(18_800, nil), qt.IsNil) // 3 900 base + 14 900 branding
		c.Assert(s.reverse(14_900), qt.IsNil)
		c.Assert(s.state().PaidCents, qt.Equals, pricing.Cents(0))
		c.Assert(s.charge(3_900, nil), qt.IsNil) // branding is the organization's now
		return topUp - 14_900 - 3_900, 3_900
	})
}

// TestReleaseOwedOrgAddOns: the startup sweep applies the add-on release a credit-back owes when the
// release itself was cut short, and leaves a release no longer owed (a payment followed the
// credit-back) and a claim taken after the credit-back alone. It is idempotent.
func TestReleaseOwedOrgAddOns(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.Address{0x17}
	setupVotingProcessOrg(c, org)
	c.Assert(testDB.CreditWallet(WalletCredit{OrgAddress: org, AmountCents: 100_000, IdempotencyKey: "cs_owed"}), qt.IsNil)
	// pay claims, debits and stamps addOn for processID under attempt
	pay := func(processID bson.ObjectID, addOn OrgAddOn, attempt int) {
		_, err := testDB.ClaimOrganizationAddOn(OrgAddOnClaim{OrgAddress: org, AddOn: addOn, ProcessID: processID})
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.DebitWalletForProcess(WalletDebit{
			OrgAddress: org, ProcessID: processID, ProcessOrgAddress: org, AmountCents: 14_900, PriceCents: 14_900,
			Attempt: attempt, AddOns: []OrgAddOn{addOn},
		}), qt.IsNil)
		_, err = testDB.SetOrganizationAddOnPaid(org, addOn, time.Now())
		c.Assert(err, qt.IsNil)
	}
	// creditBack gives processID's attempt back, owing the release of addOn, which it never applies
	creditBack := func(processID bson.ObjectID, addOn OrgAddOn, attempt int) {
		time.Sleep(2 * time.Millisecond) // the release is pinned to claims strictly before the credit-back
		c.Assert(testDB.CreditWallet(WalletCredit{
			OrgAddress: org, AmountCents: 14_900, IdempotencyKey: PublishFailCreditKey(processID, attempt),
			Kind: WalletEntryCreditBack, ProcessID: processID, ProcessOrgAddress: org, ReleaseAddOns: []OrgAddOn{addOn},
		}), qt.IsNil)
	}
	owing, repaid := bson.NewObjectID(), bson.NewObjectID()
	pay(owing, OrgAddOnBranding, 0)
	creditBack(owing, OrgAddOnBranding, 0)
	pay(repaid, OrgAddOnCustomURL, 0)
	creditBack(repaid, OrgAddOnCustomURL, 0)
	pay(repaid, OrgAddOnCustomURL, 1)

	for range 2 {
		c.Assert(testDB.ReleaseOwedOrgAddOns(), qt.IsNil)
		o, err := testDB.Organization(org)
		c.Assert(err, qt.IsNil)
		c.Assert(o.BrandingPaidAt.IsZero(), qt.IsTrue)
		claimant, _ := o.AddOnClaim(OrgAddOnBranding)
		c.Assert(claimant, qt.Equals, bson.NilObjectID)
		c.Assert(o.CustomURLPaidAt.IsZero(), qt.IsFalse)
		claimant, _ = o.AddOnClaim(OrgAddOnCustomURL)
		c.Assert(claimant, qt.Equals, repaid)
	}
}
