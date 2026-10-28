package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func newPendingPayment(processID bson.ObjectID, sessionID string) *ProcessPayment {
	return &ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        testOrgAddress,
		CheckoutSessionID: sessionID,
		AmountCents:       29_000,
		TopUpCents:        29_000,
		Currency:          "eur",
		RequestedBy:       testUserEmail,
	}
}

// TestProcessPaymentTopUpCheckout walks a top-up checkout opened for a process: only its own
// session consumes it, once; an open one is never overwritten by the paid display cache, a
// consumed one is; and an expiry drops only the session it names.
func TestProcessPaymentTopUpCheckout(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	_, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_1"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	// an open checkout is not the wallet's to pay over: the customer may still pay it
	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentPaid, AmountCents: 29_000,
	}), qt.IsNil)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPending)

	// a stale session neither consumes nor drops it
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_other")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.DeleteProcessPaymentCheckout(processID, "cs_other")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// its own session consumes it exactly once
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPending)
	c.Assert(payment.Consumed, qt.IsTrue)

	// consumed: the paid cache goes over it, keeping the session that funded it
	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentPaid, AmountCents: 29_000,
	}), qt.IsNil)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_1")
	c.Assert(payment.Consumed, qt.IsFalse)

	// paid: a replayed session consumes nothing and an expiry drops nothing
	ok, err = testDB.ConsumeProcessPaymentTopUp(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	ok, err = testDB.DeleteProcessPaymentCheckout(processID, "cs_1")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// an expired checkout goes, and only the one named
	expiredID := bson.NewObjectID()
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(expiredID, "cs_3"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.DeleteProcessPaymentCheckout(expiredID, "cs_3")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	_, err = testDB.ProcessPayment(expiredID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}

// TestProcessPaymentCache: the display cache of what a process paid is created when there is no
// row, refreshed by every money path (keeping the first paidAt), and goes with the draft.
func TestProcessPaymentCache(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentPending, AmountCents: 1,
	}), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: common.Address{}, Status: ProcessPaymentPaid, AmountCents: 1,
	}), qt.ErrorIs, ErrInvalidData)

	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentPaid, AmountCents: 14_200,
	}), qt.IsNil)
	stored, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(stored.AmountCents, qt.Equals, pricing.Cents(14_200))
	c.Assert(stored.PaidAt.IsZero(), qt.IsFalse)

	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentPaid, AmountCents: 20_000,
	}), qt.IsNil)
	raised, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(raised.AmountCents, qt.Equals, pricing.Cents(20_000))
	c.Assert(raised.CreatedAt.Equal(stored.CreatedAt), qt.IsTrue)
	c.Assert(raised.PaidAt.Equal(stored.PaidAt), qt.IsTrue)

	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: processID, OrgAddress: testOrgAddress, Status: ProcessPaymentFailed, AmountCents: 20_000,
	}), qt.IsNil)
	failed, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(failed.Status, qt.Equals, ProcessPaymentFailed)

	c.Assert(testDB.DeleteProcessPayment(processID), qt.IsNil)
	_, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	c.Assert(testDB.DeleteProcessPayment(processID), qt.IsNil) // a retry finds nothing to drop
}

// TestProcessPaymentBase: a published process's non-wallet base is written once; a process with no
// row, or a consumed top-up, gets the paid cache at it, while a row a wallet payment refreshed keeps
// its own amount.
func TestProcessPaymentBase(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	freeID, consumedID, walletID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	base := func(id bson.ObjectID, cents pricing.Cents) bool {
		ok, err := testDB.SetProcessPaymentBase(id, testOrgAddress, cents)
		c.Assert(err, qt.IsNil)
		return ok
	}
	_, err := testDB.SetProcessPaymentBase(freeID, testOrgAddress, -1)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)

	c.Assert(base(freeID, 0), qt.IsTrue)
	c.Assert(base(freeID, 3_900), qt.IsFalse) // write-once
	payment, err := testDB.ProcessPayment(freeID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.BaseCents, qt.DeepEquals, new(pricing.Cents))
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(0))

	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(consumedID, "cs_consumed"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.ConsumeProcessPaymentTopUp(consumedID, "cs_consumed")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	c.Assert(base(consumedID, 0), qt.IsTrue)
	payment, err = testDB.ProcessPayment(consumedID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, ProcessPaymentPaid)
	c.Assert(payment.Consumed, qt.IsFalse)
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(0))

	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: walletID, OrgAddress: testOrgAddress, Status: ProcessPaymentPaid, AmountCents: 3_900,
	}), qt.IsNil)
	c.Assert(base(walletID, 0), qt.IsTrue)
	payment, err = testDB.ProcessPayment(walletID)
	c.Assert(err, qt.IsNil)
	c.Assert(*payment.BaseCents, qt.Equals, pricing.Cents(0))
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(3_900))
}

// TestProcessPaymentPendingPinsReplacedSession: a pending payment is replaced only by a caller
// that observed its session, so two concurrent checkouts cannot both store.
func TestProcessPaymentPendingPinsReplacedSession(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()

	// two checkouts race, each having seen no payment at all: only one may store
	ok, err := testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_a"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_b"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_a")

	// replacing a session that is no longer the stored one is refused just the same
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_c"), "cs_gone")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// the caller that actually reconciled cs_a replaces it
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_c"), "cs_a")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	payment, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.CheckoutSessionID, qt.Equals, "cs_c")

	// a replacement whose row a late expiry dropped meanwhile does not insert a claimless row
	deleted, err := testDB.DeleteProcessPaymentCheckout(processID, "cs_c")
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsTrue)
	ok, err = testDB.SetProcessPaymentPending(newPendingPayment(processID, "cs_d"), "cs_c")
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)
	_, err = testDB.ProcessPayment(processID)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}

// TestOrganizationBrandingReliedOnPendingSibling: a branded draft whose checkout is open counts as
// relying on the organization's branding, like a paid one: its pending payment was priced without
// branding and pins that price.
func TestOrganizationBrandingReliedOnPendingSibling(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.Address{0x14}
	setupVotingProcessOrg(c, org)
	branded := func() bson.ObjectID {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, AddOns: ProcessAddOns{Branding: true}})
		c.Assert(err, qt.IsNil)
		return id
	}
	paid, sibling := branded(), branded()

	relied, err := testDB.OrganizationAddOnReliedOn(OrganizationAddOnReliedOnParams{
		Payer: org, OrgAddress: org, AddOn: OrgAddOnBranding, ExceptProcessID: paid,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(relied, qt.IsFalse) // a branded draft nobody is paying for relies on nothing

	pending := newPendingPayment(sibling, "cs_sibling")
	pending.OrgAddress = org
	stored, err := testDB.SetProcessPaymentPending(pending, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	relied, err = testDB.OrganizationAddOnReliedOn(OrganizationAddOnReliedOnParams{
		Payer: org, OrgAddress: org, AddOn: OrgAddOnBranding, ExceptProcessID: paid,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(relied, qt.IsTrue)
}

// TestOrganizationBrandingReliedOnPaidSibling: a branded draft relies on the organization's
// branding while its ledger nets a payment — whatever its payment row says, and even before the
// movement's row was posted — and stops once a credit-back settles it.
func TestOrganizationBrandingReliedOnPaidSibling(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() {
		walletStepHook = nil
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})
	org := common.Address{0x16}
	setupVotingProcessOrg(c, org)
	branded := func() bson.ObjectID {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, AddOns: ProcessAddOns{Branding: true}})
		c.Assert(err, qt.IsNil)
		return id
	}
	paid, sibling := branded(), branded()
	c.Assert(testDB.CreditWallet(WalletCredit{OrgAddress: org, AmountCents: 10_000, IdempotencyKey: "cs_relied"}), qt.IsNil)
	reliedOn := func() bool {
		relied, err := testDB.OrganizationAddOnReliedOn(OrganizationAddOnReliedOnParams{
			Payer: org, OrgAddress: org, AddOn: OrgAddOnBranding, ExceptProcessID: paid,
		})
		c.Assert(err, qt.IsNil)
		return relied
	}

	// the debit is cut short before its ledger row: it still counts
	walletStepHook = func(walletStep, string) error { return errSimulatedCrash }
	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
		OrgAddress: org, ProcessID: sibling, ProcessOrgAddress: org, AmountCents: 3_900, PriceCents: 3_900,
	}), qt.ErrorIs, errSimulatedCrash)
	walletStepHook = nil
	c.Assert(reliedOn(), qt.IsTrue)

	c.Assert(testDB.CreditWallet(WalletCredit{
		OrgAddress: org, AmountCents: 3_900, IdempotencyKey: RefundCreditKey(sibling),
		Kind: WalletEntryCreditBack, ProcessID: sibling, ProcessOrgAddress: org,
	}), qt.IsNil)
	c.Assert(reliedOn(), qt.IsFalse)

	// the paid test runs under the payer's wallet lock: it must be served by the
	// {orgAddress, processId} index, never scan the ledger
	var plan bson.M
	c.Assert(testDB.walletLedger.Database().RunCommand(context.Background(), bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "aggregate", Value: testDB.walletLedger.Name()},
			{Key: "pipeline", Value: bson.A{bson.M{"$match": processMovementsMatch(org, bson.A{paid, sibling})}}},
			{Key: "cursor", Value: bson.M{}},
		}},
		{Key: "verbosity", Value: "queryPlanner"},
	}).Decode(&plan), qt.IsNil)
	explained, err := bson.MarshalExtJSON(plan, false, false)
	c.Assert(err, qt.IsNil)
	c.Assert(string(explained), qt.Contains, "orgAddress_1_processId_1")
	c.Assert(string(explained), qt.Not(qt.Contains), "COLLSCAN")
}

// TestOrganizationBrandingReliedOnDraftBeingPublished: a branded draft holding the publish claim,
// or with elections already mined, relies on the organization's branding before it has a payment
// of its own (a free one gets its record only once published), and a draft that only holds the
// claim stops relying on it once the claim is cleared.
func TestOrganizationBrandingReliedOnDraftBeingPublished(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.Address{0x15}
	setupVotingProcessOrg(c, org)
	branded := func() bson.ObjectID {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, AddOns: ProcessAddOns{Branding: true}})
		c.Assert(err, qt.IsNil)
		return id
	}
	reliedOn := func(except bson.ObjectID) bool {
		relied, err := testDB.OrganizationAddOnReliedOn(OrganizationAddOnReliedOnParams{
			Payer: org, OrgAddress: org, AddOn: OrgAddOnBranding, ExceptProcessID: except,
		})
		c.Assert(err, qt.IsNil)
		return relied
	}
	paid, publishing, resumed := branded(), branded(), branded()
	c.Assert(reliedOn(paid), qt.IsFalse)

	owner, claimed, err := testDB.ClaimVotingProcessForPublish(publishing, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(reliedOn(paid), qt.IsTrue)
	c.Assert(reliedOn(publishing), qt.IsFalse) // its own claim does not count
	c.Assert(testDB.ClearVotingProcessPublishing(publishing, owner), qt.IsNil)
	c.Assert(reliedOn(paid), qt.IsFalse)

	c.Assert(testDB.MarkVotingProcessMined(resumed), qt.IsNil)
	c.Assert(reliedOn(paid), qt.IsTrue)
}

// TestOrganizationBrandingDelivered: the teardown test counts only a branded sibling the add-on
// reached — published, or with elections mined — never a draft that is paid, being paid or being
// published, all of which the same teardown deletes.
func TestOrganizationBrandingDelivered(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org := common.Address{0x17}
	setupVotingProcessOrg(c, org)
	branded := func() bson.ObjectID {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, AddOns: ProcessAddOns{Branding: true}})
		c.Assert(err, qt.IsNil)
		return id
	}
	delivered := func(except bson.ObjectID) bool {
		got, err := testDB.OrganizationAddOnDelivered(org, OrgAddOnBranding, except)
		c.Assert(err, qt.IsNil)
		return got
	}
	payer, pending, publishing, mined := branded(), branded(), branded(), branded()

	stored, err := testDB.SetProcessPaymentPending(newPendingPayment(pending, "cs_delivered"), "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	_, claimed, err := testDB.ClaimVotingProcessForPublish(publishing, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	relied, err := testDB.OrganizationAddOnReliedOn(OrganizationAddOnReliedOnParams{
		Payer: org, OrgAddress: org, AddOn: OrgAddOnBranding, ExceptProcessID: payer,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(relied, qt.IsTrue)
	c.Assert(delivered(payer), qt.IsFalse)

	c.Assert(testDB.MarkVotingProcessMined(mined), qt.IsNil)
	c.Assert(delivered(payer), qt.IsTrue)
	c.Assert(delivered(mined), qt.IsFalse) // its own elections do not count
	_, err = testDB.OrganizationAddOnDelivered(org, OrgAddOn("bogus"), payer)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}
