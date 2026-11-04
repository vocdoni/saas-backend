package main

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/test"
)

// TestRun: a dry run deletes nothing; a possibly live election refuses the erasure, erasing
// nothing, unless -force; the erasure credits a paid draft back before it goes; an organization
// managing others is refused even with -force.
func TestRun(t *testing.T) {
	c := qt.New(t)
	t.Setenv("VOCDONI_MONGO_RESET_DB", "")
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	database := test.RandomDatabaseName()
	ms, err := db.New(uri, database)
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)

	org, managed := common.Address{0x61}, common.Address{0x62}
	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Country: "ES"}), qt.IsNil)
	c.Assert(ms.CreditWallet(db.WalletCredit{OrgAddress: org, AmountCents: 10_000, IdempotencyKey: "cs_orgdel"}), qt.IsNil)
	draft, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: org, ProcessID: draft, ProcessOrgAddress: org, AmountCents: 2_000, PriceCents: 2_000,
	}), qt.IsNil)
	published, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org, Published: true})
	c.Assert(err, qt.IsNil)
	_, err = ms.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: published, OrgAddress: org, UpstreamID: internal.HexBytes{0x01}, Status: db.QuestionStatusReady,
	})
	c.Assert(err, qt.IsNil)
	opts := &options{mongoURL: uri, database: database, org: org.Hex(), yes: true}

	opts.dryRun = true
	c.Assert(run(opts), qt.IsNil)
	opts.dryRun = false
	c.Assert(run(opts), qt.ErrorMatches, ".*1 elections may still be live.*-force.*")
	_, err = ms.VotingProcess(draft)
	c.Assert(err, qt.IsNil)
	wallet, err := ms.Wallet(org)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(8_000))

	opts.force = true
	c.Assert(run(opts), qt.IsNil)
	_, err = ms.Organization(org)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = ms.VotingProcess(draft)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	wallet, err = ms.Wallet(org)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000)) // credited back, kept

	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Country: "ES", Integrator: true}), qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: managed, Country: "ES", ManagedBy: org}), qt.IsNil)
	c.Assert(run(opts), qt.ErrorMatches, ".*manages 1 organizations.*")
	_, err = ms.Organization(org)
	c.Assert(err, qt.IsNil)
}
