package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
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
		ProcessID: checkoutDraft, OrgAddress: org, CheckoutSessionID: "cs_userdel_open",
		AmountCents: 1_716, TopUpCents: 1_716, Currency: "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	c.Assert(deleteDrafts(&bytes.Buffer{}, ms, user), qt.IsNil)
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

// TestEraseIntegratorWithManagedOrgs: erasing the sole admin of an integrator and of one of its
// managed organizations deletes every draft before printing any balance, so the integrator's
// balance printed already holds the credit-back of the managed organization's paid draft; and the
// managed organization's spending under the integrator's cap goes with it.
func TestEraseIntegratorWithManagedOrgs(t *testing.T) {
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

	integrator, managed := common.Address{0x51}, common.Address{0x52}
	user := &db.User{
		Email: "userdel-integrator@test.com", Password: "pass", FirstName: "User", LastName: "Del", Verified: true,
		// the integrator first: its balance would be printed before the managed draft is credited back
		Organizations: []db.OrganizationUser{{Address: integrator, Role: db.AdminRole}, {Address: managed, Role: db.AdminRole}},
	}
	user.ID, err = ms.SetUser(user)
	c.Assert(err, qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: integrator, Creator: user.Email, Integrator: true}), qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: managed, Creator: user.Email, ManagedBy: integrator}), qt.IsNil)
	capCents := pricing.Cents(5_000)
	c.Assert(ms.SetManagedOrgSpendCap(integrator, managed, &capCents), qt.IsNil)

	// the managed organization's draft paid 2 000 from the integrator's wallet of 10 000
	c.Assert(ms.CreditWallet(db.WalletCredit{
		OrgAddress: integrator, AmountCents: 10_000, IdempotencyKey: "cs_userdel_int",
	}), qt.IsNil)
	draft, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: managed})
	c.Assert(err, qt.IsNil)
	c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integrator, ProcessID: draft, ProcessOrgAddress: managed, AmountCents: 2_000, PriceCents: 2_000,
		ManagedOrg: managed,
	}), qt.IsNil)

	var out bytes.Buffer
	c.Assert(deleteDrafts(&out, ms, user), qt.IsNil)
	c.Assert(out.String(), qt.Contains, "org "+integrator.String()+"\n    wallet balance: 10000 eur cents")

	_, err = ms.EraseUser(user.ID)
	c.Assert(err, qt.IsNil)
	wallet, err := ms.Wallet(integrator)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(10_000)) // kept, to be returned by hand
	c.Assert(wallet.ManagedOrgSpend(managed), qt.DeepEquals, db.ManagedOrgSpend{})
}

// TestDeleteDraftsReturnsAddOnsNobodyGot: tearing an organization down withholds a
// once-per-organization add-on from a draft's credit-back only for a sibling it was delivered to.
// A kept-paid draft that bought branding and a paid draft riding on its stamp both go, in either
// order, and the payer gets back everything it paid; a published branded sibling keeps it, and so
// does a rider with elections mined, in either order (its own base stays paid, delivered).
func TestDeleteDraftsReturnsAddOnsNobodyGot(t *testing.T) {
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

	const funded, riderCents = pricing.Cents(20_000), pricing.Cents(2_000)
	for i, tc := range []struct {
		name                      string
		riderFirst, shared, mined bool
		want                      pricing.Cents
	}{
		{name: "payer first", want: funded},
		{name: "rider first", riderFirst: true, want: funded},
		{name: "published sibling", shared: true, want: funded - pricing.BrandingCents},
		{name: "mined rider last", mined: true, want: funded - riderCents - pricing.BrandingCents},
		{name: "mined rider first", riderFirst: true, mined: true, want: funded - riderCents - pricing.BrandingCents},
	} {
		c.Run(tc.name, func(c *qt.C) {
			org := common.Address{0x60, byte(i)}
			user := &db.User{
				Email: fmt.Sprintf("userdel-addons-%d@test.com", i), Password: "pass", FirstName: "User", LastName: "Del",
				Verified: true, Organizations: []db.OrganizationUser{{Address: org, Role: db.AdminRole}},
			}
			user.ID, err = ms.SetUser(user)
			c.Assert(err, qt.IsNil)
			c.Assert(ms.SetOrganization(&db.Organization{Address: org, Creator: user.Email, Country: "ES"}), qt.IsNil)
			c.Assert(ms.CreditWallet(db.WalletCredit{
				OrgAddress: org, AmountCents: funded, IdempotencyKey: fmt.Sprintf("cs_userdel_addons_%d", i),
			}), qt.IsNil)
			branded := func() bson.ObjectID {
				id, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org, AddOns: db.ProcessAddOns{Branding: true}})
				c.Assert(err, qt.IsNil)
				return id
			}
			// the payer bought only branding (kept paid after a failed publish); the rider paid its
			// base on the strength of the stamp; the list order is the insertion order
			var payer, rider bson.ObjectID
			if tc.riderFirst {
				rider, payer = branded(), branded()
			} else {
				payer, rider = branded(), branded()
			}
			claimedAt, err := ms.ClaimOrganizationAddOn(db.OrgAddOnClaim{
				OrgAddress: org, AddOn: db.OrgAddOnBranding, ProcessID: payer,
			})
			c.Assert(err, qt.IsNil)
			c.Assert(claimedAt.IsZero(), qt.IsFalse)
			c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
				OrgAddress: org, ProcessID: payer, ProcessOrgAddress: org, AmountCents: pricing.BrandingCents,
				PriceCents: pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding},
			}), qt.IsNil)
			_, err = ms.SetOrganizationAddOnPaid(org, db.OrgAddOnBranding, time.Now())
			c.Assert(err, qt.IsNil)
			c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
				OrgAddress: org, ProcessID: rider, ProcessOrgAddress: org, AmountCents: riderCents, PriceCents: riderCents,
			}), qt.IsNil)
			if tc.mined {
				c.Assert(ms.MarkVotingProcessMined(rider), qt.IsNil)
			}
			if tc.shared {
				_, err := ms.SetVotingProcess(&db.VotingProcess{
					OrgAddress: org, AddOns: db.ProcessAddOns{Branding: true}, Published: true,
				})
				c.Assert(err, qt.IsNil)
			}

			c.Assert(deleteDrafts(&bytes.Buffer{}, ms, user), qt.IsNil)
			n, err := ms.CountVotingProcesses(org, db.DraftOnly)
			c.Assert(err, qt.IsNil)
			c.Assert(n, qt.Equals, int64(0))
			wallet, err := ms.Wallet(org)
			c.Assert(err, qt.IsNil)
			c.Assert(wallet.BalanceCents, qt.Equals, tc.want)
		})
	}
}

// TestDeleteDraftsCreditsWithheldAddOnBack: a branding price an earlier credit-back withheld (a
// sibling relied on it) stayed the organization's stamp. Tearing the organization down credits it
// back once when no published or mined process received it, and clears the stamp; a published
// branded sibling keeps it, and so does a mined draft a crashed attempt already dropped, since the
// attempt recorded it first. A retry credits nothing twice. A teardown cut between clearing the stamp
// and the credit leaves no stamp to ride: a branded sibling published meanwhile pays its own
// branding, and the retry still credits the withheld one, once. A sibling that instead buys the
// branding again and has it withheld again is a second price: the retry credits both, once each.
func TestDeleteDraftsCreditsWithheldAddOnBack(t *testing.T) {
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

	const funded, baseCents, riderCents = pricing.Cents(50_000), pricing.Cents(1_000), pricing.Cents(2_000)
	for i, tc := range []struct {
		name                                string
		published, droppedMined, retry, cut bool
		rebought                            bool
		want                                pricing.Cents
		credited                            bool
	}{
		{name: "nobody got it", want: funded, credited: true},
		{name: "published sibling", published: true, want: funded - pricing.BrandingCents},
		{name: "mined draft dropped by a crashed attempt", droppedMined: true, want: funded - pricing.BrandingCents},
		{name: "retry", retry: true, want: funded, credited: true},
		{name: "cut before the credit", cut: true, want: funded - baseCents - pricing.BrandingCents, credited: true},
		{name: "cut, then bought and withheld again", rebought: true, want: funded, credited: true},
	} {
		c.Run(tc.name, func(c *qt.C) {
			org := common.Address{0x61, byte(i)}
			user := &db.User{
				Email: fmt.Sprintf("userdel-withheld-%d@test.com", i), Password: "pass", FirstName: "User", LastName: "Del",
				Verified: true, Organizations: []db.OrganizationUser{{Address: org, Role: db.AdminRole}},
			}
			user.ID, err = ms.SetUser(user)
			c.Assert(err, qt.IsNil)
			c.Assert(ms.SetOrganization(&db.Organization{Address: org, Creator: user.Email, Country: "ES"}), qt.IsNil)
			c.Assert(ms.CreditWallet(db.WalletCredit{
				OrgAddress: org, AmountCents: funded, IdempotencyKey: fmt.Sprintf("cs_userdel_withheld_%d", i),
			}), qt.IsNil)
			branded := func(vp db.VotingProcess) bson.ObjectID {
				vp.OrgAddress, vp.AddOns = org, db.ProcessAddOns{Branding: true}
				id, err := ms.SetVotingProcess(&vp)
				c.Assert(err, qt.IsNil)
				return id
			}
			// the payer bought base and branding, and was deleted while the rider relied on the
			// stamp: its credit-back withheld the branding and it is gone
			payer := branded(db.VotingProcess{})
			c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
				OrgAddress: org, ProcessID: payer, ProcessOrgAddress: org, AmountCents: baseCents + pricing.BrandingCents,
				PriceCents: baseCents + pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding},
			}), qt.IsNil)
			_, err = ms.SetOrganizationAddOnPaid(org, db.OrgAddOnBranding, time.Now())
			c.Assert(err, qt.IsNil)
			rider := branded(db.VotingProcess{})
			c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
				OrgAddress: org, ProcessID: rider, ProcessOrgAddress: org, AmountCents: riderCents, PriceCents: riderCents,
			}), qt.IsNil)
			c.Assert(ms.CreditWallet(db.WalletCredit{
				OrgAddress: org, AmountCents: baseCents, IdempotencyKey: db.RefundCreditKey(payer),
				Kind: db.WalletEntryCreditBack, ProcessID: payer, ProcessOrgAddress: org,
				Withheld: db.RefundWithholding{Cents: pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding}},
			}), qt.IsNil)
			c.Assert(ms.DeleteVotingProcess(payer), qt.IsNil)
			if tc.published {
				branded(db.VotingProcess{Published: true})
			}
			if tc.droppedMined {
				mined := branded(db.VotingProcess{})
				c.Assert(ms.MarkVotingProcessMined(mined), qt.IsNil)
				delivered, err := ms.RecordOrganizationAddOnsDelivered(org)
				c.Assert(err, qt.IsNil)
				c.Assert(delivered, qt.DeepEquals, []db.OrgAddOn{db.OrgAddOnBranding})
				c.Assert(ms.DeleteVotingProcess(mined), qt.IsNil)
			}

			if tc.cut {
				// the teardown's first write lands and it stops before the credit: the stamp is gone,
				// so a branded sibling published meanwhile is priced (and pays) its own branding
				refund, err := ms.WalletEntry(org, db.RefundCreditKey(payer))
				c.Assert(err, qt.IsNil)
				c.Assert(ms.OweOrganizationAddOnCredit(org, db.OrgAddOnBranding, refund), qt.IsNil)
				o, err := ms.Organization(org)
				c.Assert(err, qt.IsNil)
				c.Assert(o.BrandingPaidAt.IsZero(), qt.IsTrue)
				sibling := branded(db.VotingProcess{Published: true})
				c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
					OrgAddress: org, ProcessID: sibling, ProcessOrgAddress: org, AmountCents: baseCents + pricing.BrandingCents,
					PriceCents: baseCents + pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding},
				}), qt.IsNil)
				time.Sleep(2 * time.Millisecond) // the sibling's stamp is newer than the withholding
				_, err = ms.SetOrganizationAddOnPaid(org, db.OrgAddOnBranding, time.Now())
				c.Assert(err, qt.IsNil)
			}

			var resold bson.ObjectID
			if tc.rebought {
				// the cut as above, then a draft buys the branding again (stamped after the first
				// withholding) and is deleted while a sibling relies on it: withheld a second time
				refund, err := ms.WalletEntry(org, db.RefundCreditKey(payer))
				c.Assert(err, qt.IsNil)
				c.Assert(ms.OweOrganizationAddOnCredit(org, db.OrgAddOnBranding, refund), qt.IsNil)
				resold = branded(db.VotingProcess{})
				c.Assert(ms.DebitWalletForProcess(db.WalletDebit{
					OrgAddress: org, ProcessID: resold, ProcessOrgAddress: org, AmountCents: baseCents + pricing.BrandingCents,
					PriceCents: baseCents + pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding},
				}), qt.IsNil)
				time.Sleep(2 * time.Millisecond)
				_, err = ms.SetOrganizationAddOnPaid(org, db.OrgAddOnBranding, time.Now())
				c.Assert(err, qt.IsNil)
				time.Sleep(2 * time.Millisecond)
				c.Assert(ms.CreditWallet(db.WalletCredit{
					OrgAddress: org, AmountCents: baseCents, IdempotencyKey: db.RefundCreditKey(resold),
					Kind: db.WalletEntryCreditBack, ProcessID: resold, ProcessOrgAddress: org,
					Withheld: db.RefundWithholding{Cents: pricing.BrandingCents, AddOns: []db.OrgAddOn{db.OrgAddOnBranding}},
				}), qt.IsNil)
				c.Assert(ms.DeleteVotingProcess(resold), qt.IsNil)
			}

			c.Assert(deleteDrafts(&bytes.Buffer{}, ms, user), qt.IsNil)
			if tc.retry {
				c.Assert(deleteDrafts(&bytes.Buffer{}, ms, user), qt.IsNil)
			}
			wallet, err := ms.Wallet(org)
			c.Assert(err, qt.IsNil)
			c.Assert(wallet.BalanceCents, qt.Equals, tc.want)
			if tc.rebought {
				again, err := ms.WalletEntry(org, db.TeardownAddOnCreditKey(org, db.OrgAddOnBranding, resold))
				c.Assert(err, qt.IsNil)
				c.Assert(again.AmountCents, qt.Equals, pricing.BrandingCents)
				c.Assert(again.ProcessID, qt.Equals, resold)
			}
			credit, err := ms.WalletEntry(org, db.TeardownAddOnCreditKey(org, db.OrgAddOnBranding, payer))
			o, oerr := ms.Organization(org)
			c.Assert(oerr, qt.IsNil)
			c.Assert(o.AddOnsOwedCredit, qt.HasLen, 0)
			// the stamp stays while a published or mined process holds the add-on
			c.Assert(o.BrandingPaidAt.IsZero(), qt.Equals, !tc.published && !tc.droppedMined && !tc.cut)
			if !tc.credited {
				c.Assert(err, qt.ErrorIs, db.ErrNotFound)
				return
			}
			c.Assert(err, qt.IsNil)
			c.Assert(credit.AmountCents, qt.Equals, pricing.BrandingCents)
			c.Assert(credit.Kind, qt.Equals, db.WalletEntryCreditBack)
			c.Assert(credit.ProcessID, qt.Equals, payer)
			c.Assert(credit.ProcessOrgAddress, qt.Equals, org)
			// the payer's own ledger is settled by its refund row; the teardown credit is not part of it
			state, err := ms.ProcessLedgerState(org, payer)
			c.Assert(err, qt.IsNil)
			c.Assert(state.PaidCents, qt.Equals, pricing.Cents(0))
		})
	}
}
