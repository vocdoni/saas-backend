package topup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"github.com/vocdoni/saas-backend/test"
)

// day is the calendar day n days from today, as a credit's received date carries it.
func day(n int) time.Time { return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, n) }

// TestPrepareCredit (AC3, AC14): a deferred credit needs the gross received (at least the amount)
// and a calendar day from 90 days ago to tomorrow (UTC); an external one neither. Both follow
// Validate's amount and description rules and need an organization with a wallet of its own.
// Every refusal of the request is ErrInvalid; nothing reaches Stripe (there is no client).
func TestPrepareCredit(t *testing.T) {
	c := qt.New(t)
	f, _, ms := newFlow(t)
	deferred := CreditRequest{
		Org: flowOrg, AmountCents: 50_000, ReceivedCents: 60_500, ReceivedAt: day(-3), Description: " transfer ",
	}
	plan, err := PrepareCredit(ms, deferred)
	c.Assert(err, qt.IsNil)
	c.Assert(plan, qt.DeepEquals, &CreditPlan{
		Org: flowOrg, AmountCents: 50_000, ReceivedCents: 60_500, ReceivedAt: day(-3), Description: "transfer",
	})
	external := CreditRequest{Org: flowOrg, AmountCents: 50_000, Description: "transfer", External: true}
	plan, err = PrepareCredit(ms, external)
	c.Assert(err, qt.IsNil)
	c.Assert(plan, qt.DeepEquals, &CreditPlan{Org: flowOrg, AmountCents: 50_000, Description: "transfer", External: true})
	for _, ok := range []func(r *CreditRequest){
		func(r *CreditRequest) { r.ReceivedCents = r.AmountCents },
		func(r *CreditRequest) { r.ReceivedAt = day(1) },
		func(r *CreditRequest) { r.ReceivedAt = day(-90) },
	} {
		r := deferred
		ok(&r)
		_, err := PrepareCredit(ms, r)
		c.Assert(err, qt.IsNil)
	}

	for name, tc := range map[string]struct {
		base   CreditRequest
		mutate func(r *CreditRequest)
	}{
		"below €1":          {deferred, func(r *CreditRequest) { r.AmountCents = pricing.MinimumChargeCents - 1 }},
		"above the maximum": {deferred, func(r *CreditRequest) { r.AmountCents, r.ReceivedCents = MaxAmountCents+1, MaxAmountCents+1 }},
		"no description":    {deferred, func(r *CreditRequest) { r.Description = " " }},
		"long description":  {external, func(r *CreditRequest) { r.Description = strings.Repeat("x", MaxDescriptionChars+1) }},
		"received below":    {deferred, func(r *CreditRequest) { r.ReceivedCents = r.AmountCents - 1 }},
		"no received date":  {deferred, func(r *CreditRequest) { r.ReceivedAt = time.Time{} }},
		"a time of day":     {deferred, func(r *CreditRequest) { r.ReceivedAt = day(-1).Add(time.Hour) }},
		"in two days":       {deferred, func(r *CreditRequest) { r.ReceivedAt = day(2) }},
		"91 days ago":       {deferred, func(r *CreditRequest) { r.ReceivedAt = day(-91) }},
		"external received": {external, func(r *CreditRequest) { r.ReceivedCents = 60_500 }},
		"external date":     {external, func(r *CreditRequest) { r.ReceivedAt = day(-1) }},
	} {
		r := tc.base
		tc.mutate(&r)
		_, err := PrepareCredit(ms, r)
		c.Assert(err, qt.ErrorIs, ErrInvalid, qt.Commentf("%s", name))
	}
	managed := external
	managed.Org = flowManaged
	_, err = PrepareCredit(ms, managed)
	c.Assert(err, qt.ErrorIs, ErrManagedOrg)
	unknown := external
	unknown.Org = flowOther
	_, err = PrepareCredit(ms, unknown)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	c.Assert(f.calls, qt.HasLen, 0)
}

// TestListDuplicates (AC14): the duplicates a direct credit sees are Prepare's: the org's manual
// top-up invoices, not void, with the same net amount and description; read only.
func TestListDuplicates(t *testing.T) {
	c := qt.New(t)
	f, _, _ := newFlow(t)
	f.listed = manualInvoiceJSON("in_dup", "paid", flowOrg, "50000", "transfer") + "," +
		manualInvoiceJSON("in_void", "void", flowOrg, "50000", "transfer") + "," +
		manualInvoiceJSON("in_other", "open", flowOther, "50000", "transfer") + "," +
		manualInvoiceJSON("in_amount", "open", flowOrg, "40000", "transfer")
	got, err := ListDuplicates(context.Background(), DuplicateQuery{Org: flowOrg, AmountCents: 50_000, Description: "transfer"})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 1)
	c.Assert(got[0].ID, qt.Equals, "in_dup")
	f.fail = "/v1/invoices"
	_, err = ListDuplicates(context.Background(), DuplicateQuery{Org: flowOrg, AmountCents: 50_000, Description: "transfer"})
	c.Assert(err, qt.ErrorMatches, "could not list the invoices of the last 7 days.*")
	c.Assert(f.writes(), qt.HasLen, 0)
}

// TestCredit (AC3, AC14): a credit lands once per key as a top-up row, whatever retries follow;
// a deferred plan takes only a manualcredit key and an external plan only an externalcredit key,
// each a prefix and a 24-hex id, and a wrong key credits nothing (Mongo testcontainer).
func TestCredit(t *testing.T) {
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
	org := common.Address{0x51}
	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Type: db.CompanyType}), qt.IsNil)
	balance := func() pricing.Cents {
		wallet, err := ms.Wallet(org)
		c.Assert(err, qt.IsNil)
		return wallet.BalanceCents
	}
	deferred := &CreditPlan{Org: org, AmountCents: 50_000, ReceivedCents: 60_500, ReceivedAt: day(-1), Description: "transfer"}
	external := &CreditPlan{Org: org, AmountCents: 20_000, Description: "transfer", External: true}
	manualKey := stripe.ManualCreditKeyPrefix + "0123456789abcdef01234567"
	externalKey := stripe.ExternalCreditKeyPrefix + "89abcdef0123456701234567"

	for _, tc := range []struct {
		plan *CreditPlan
		key  string
	}{
		{deferred, externalKey},
		{external, manualKey},
		{deferred, "cs_1"},
		{deferred, stripe.ManualCreditKeyPrefix + "1"},
		{external, stripe.ExternalCreditKeyPrefix + "0123456789ABCDEF01234567"},
	} {
		c.Assert(Credit(ms, tc.plan, tc.key), qt.ErrorIs, ErrInvalid, qt.Commentf("key %s", tc.key))
	}
	c.Assert(balance(), qt.Equals, pricing.Cents(0))

	for range 2 {
		c.Assert(Credit(ms, deferred, manualKey), qt.IsNil)
		c.Assert(Credit(ms, external, externalKey), qt.IsNil)
	}
	c.Assert(balance(), qt.Equals, pricing.Cents(70_000))
	for key, amount := range map[string]pricing.Cents{manualKey: 50_000, externalKey: 20_000} {
		entry, err := ms.WalletEntry(org, key)
		c.Assert(err, qt.IsNil)
		c.Assert(entry.Kind, qt.Equals, db.WalletEntryTopUp)
		c.Assert(entry.AmountCents, qt.Equals, amount)
	}
}
