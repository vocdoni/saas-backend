package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

// TestDeleteDrafts: with no Stripe access the drafts go, an open top-up checkout's included (its
// session is left to expire), and the organization's own wallet balance is printed, to be
// returned by hand; it never blocks.
func TestDeleteDrafts(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	ms, err := db.New(uri, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)

	org := common.Address{0x42}
	user := &db.User{
		Email: "userdel@test.com", Password: "pass", FirstName: "User", LastName: "Del", Verified: true,
		Organizations: []db.OrganizationUser{{Address: org, Role: db.AdminRole}},
	}
	user.ID, err = ms.SetUser(user)
	c.Assert(err, qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Creator: user.Email, Country: "ES"}), qt.IsNil)
	freeDraft, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	checkoutDraft, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	stored, err := ms.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID: checkoutDraft, OrgAddress: org, Payer: org, CheckoutSessionID: "cs_userdel_open",
		AmountCents: 1_716, TopUpCents: 1_716, Currency: "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	c.Assert(deleteDrafts(ms, user), qt.IsNil)
	n, err := ms.CountVotingProcesses(org, db.DraftOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))
	_, err = ms.VotingProcess(freeDraft)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = ms.ProcessPayment(checkoutDraft)
	c.Assert(err, qt.Equals, db.ErrNotFound)

	// an empty wallet prints nothing; a funded one prints its balance
	var out bytes.Buffer
	c.Assert(printWalletBalance(&out, ms, org), qt.IsNil)
	c.Assert(out.String(), qt.Equals, "")
	c.Assert(ms.CreditWallet(db.WalletCredit{
		OrgAddress: org, AmountCents: 2_500, IdempotencyKey: "cs_userdel_topup",
	}), qt.IsNil)
	c.Assert(printWalletBalance(&out, ms, org), qt.IsNil)
	c.Assert(out.String(), qt.Contains, "wallet balance: 2500 eur cents (kept; return it by hand)")
}
