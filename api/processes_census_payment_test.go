package api

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/util"
)

// fundWallet credits cents to a wallet, as a verified top-up would.
func fundWallet(t *testing.T, wallet common.Address, cents pricing.Cents) {
	t.Helper()
	if cents <= 0 {
		return
	}
	qt.Assert(t, testDB.CreditWallet(db.WalletCredit{
		OrgAddress: wallet, AmountCents: cents, IdempotencyKey: "cs_fund_" + util.RandomHex(8),
	}), qt.IsNil)
}
