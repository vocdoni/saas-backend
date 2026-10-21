package db

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestManagedOrgSpendCap: a managed organization's debits count towards the cap its integrator
// set — in the same write as the balance, so concurrent debits cannot pass it together — a credit
// for its process frees room again, a nil cap is unlimited, and a cap can be set before the wallet
// exists.
func TestManagedOrgSpendCap(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	managed, other := common.Address{0x31}, common.Address{0x32}
	spend := func(org common.Address) ManagedOrgSpend {
		wallet, err := testDB.Wallet(testOrgAddress)
		c.Assert(err, qt.IsNil)
		return wallet.ManagedOrgSpend(org)
	}
	debit := func(org common.Address, cents pricing.Cents) error {
		return testDB.DebitWalletForProcess(WalletDebit{
			OrgAddress: testOrgAddress, ProcessID: bson.NewObjectID(), AmountCents: cents, PriceCents: cents,
			ManagedOrg: org,
		})
	}

	// a cap set before the first top-up creates the wallet
	capCents := pricing.Cents(3_000)
	c.Assert(testDB.SetManagedOrgSpendCap(testOrgAddress, managed, &capCents), qt.IsNil)
	c.Assert(*spend(managed).CapCents, qt.Equals, capCents)
	c.Assert(topUpTestWallet(100_000, "cs_spend_cap_topup"), qt.IsNil)

	// concurrent debits: exactly as many as fit under the cap go through
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() { errs <- debit(managed, 1_000) })
	}
	wg.Wait()
	close(errs)
	var refused int
	for err := range errs {
		if err != nil {
			c.Assert(err, qt.Equals, ErrManagedOrgSpendCapReached)
			refused++
		}
	}
	c.Assert(refused, qt.Equals, 2)
	c.Assert(spend(managed).SpentCents, qt.Equals, pricing.Cents(3_000))
	wallet, err := testDB.Wallet(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(97_000))

	// another managed organization has no cap
	c.Assert(debit(other, 50_000), qt.IsNil)
	c.Assert(spend(other).CapCents, qt.IsNil)

	// a credit for one of its processes frees room
	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: testOrgAddress, AmountCents: 1_000, IdempotencyKey: "refund:spend-cap",
		Kind: WalletEntryCreditBack, SpentBy: managed,
	}), qt.IsNil)
	c.Assert(spend(managed).SpentCents, qt.Equals, pricing.Cents(2_000))
	c.Assert(debit(managed, 1_000), qt.IsNil)

	// a short balance is still told apart from the cap
	c.Assert(testDB.SetManagedOrgSpendCap(testOrgAddress, managed, nil), qt.IsNil)
	c.Assert(spend(managed).CapCents, qt.IsNil)
	c.Assert(debit(managed, 1_000_000), qt.Equals, ErrInsufficientWalletBalance)

	negative := pricing.Cents(-1)
	c.Assert(testDB.SetManagedOrgSpendCap(testOrgAddress, managed, &negative), qt.Equals, ErrInvalidData)
}
