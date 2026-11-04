package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// newManagedDraft creates a priced draft of fresh members (email 2FA: integratorProcessCents) in
// the managed organization and returns its id.
func newManagedDraft(t *testing.T, token string, managedAddr common.Address) bson.ObjectID {
	t.Helper()
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token,
		newVotingProcessRequest(managedAddr, postNewOrgMembers(t, token, managedAddr, pricedProcessVoters)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	qt.Assert(t, err, qt.IsNil)
	return oid
}

// debitManagedDraft pays a managed organization's draft from its integrator's wallet, as its
// publish does.
func debitManagedDraft(c *qt.C, draft bson.ObjectID) {
	vp, err := testDB.VotingProcess(draft)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)
}

// TestDeleteManagedOrgRefundsDrafts: a managed organization's teardown deletes its drafts the way
// DELETE /processes/{id} does — the wallet-paid one credited back to the integrator, with no
// Stripe call — before anything else, then removes its published voting processes too; the wallet
// ledger stays as the audit trail.
func TestDeleteManagedOrgRefundsDrafts(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "teardownrefund12")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_teardown_refund")
	fundWallet(t, integratorAddr, 100_000)

	walletPaid := newManagedDraft(t, token, managedAddr)
	debitManagedDraft(c, walletPaid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	// a published process, with a terminal question so the active-election guard lets it go
	published, err := testDB.SetVotingProcess(&db.VotingProcess{OrgAddress: managedAddr, Published: true})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: published, OrgAddress: managedAddr, Status: db.QuestionStatusCanceled,
	})
	c.Assert(err, qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil,
		"integrator", "organizations", managedAddr.String())

	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	// the deleted organization's spend entry leaves the integrator's wallet with it
	torn, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(torn.OrgSpend, qt.HasLen, 0)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, managedAddr)
	c.Assert(fake.created, qt.HasLen, 0)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, db.RefundCreditKey(walletPaid))
	_, err = testDB.ProcessPayment(walletPaid)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = testDB.VotingProcess(walletPaid)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = testDB.VotingProcess(published)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	questions, err := testDB.QuestionsByProcess(published)
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 0)
	_, err = testDB.Organization(managedAddr)
	c.Assert(err, qt.Equals, db.ErrNotFound)
}

// TestDeleteManagedOrgAbortsOnUndeletableDraft: a draft whose open checkout cannot be reconciled
// with Stripe cannot be deleted, so the teardown stops with its error and the organization stays
// whole.
func TestDeleteManagedOrgAbortsOnUndeletableDraft(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "teardownabort123")
	_, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_teardown_abort")
	draft := newManagedDraft(t, token, managedAddr)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID: draft, OrgAddress: managedAddr,
		CheckoutSessionID: "cs_unknown_" + draft.Hex(),
		AmountCents:       integratorProcessCents, TopUpCents: integratorProcessCents, Currency: "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, token, nil,
		"integrator", "organizations", managedAddr.String())
	_, err = testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	_, err = testDB.VotingProcess(draft)
	c.Assert(err, qt.IsNil)
	count, err := testDB.CountOrgMembers(managedAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(count > 0, qt.IsTrue)
}

// TestDeleteOrgDraftsForErasure: the erasure command's entry point deletes an organization's
// drafts with no payment gateway: a wallet-paid draft is credited back to its payer, a draft with
// an open checkout goes with its payment row (the session is left to expire), and a draft whose
// failed publish was credited back already is not credited again.
func TestDeleteOrgDraftsForErasure(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "erasedrafts12345")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_erase_drafts")
	fundWallet(t, integratorAddr, 100_000)

	walletPaid := newManagedDraft(t, token, managedAddr)
	debitManagedDraft(c, walletPaid)

	withCheckout := newManagedDraft(t, token, managedAddr)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID: withCheckout, OrgAddress: managedAddr,
		CheckoutSessionID: "cs_erase_open_" + withCheckout.Hex(),
		AmountCents:       integratorProcessCents, TopUpCents: integratorProcessCents, Currency: "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	reversed := newManagedDraft(t, token, managedAddr)
	debitManagedDraft(c, reversed)
	c.Assert(testAPI.reversePublishDebit(reversed), qt.IsNil)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	c.Assert(DeleteOrgDrafts(testDB, managedAddr), qt.IsNil)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	assertWallet(t, integratorAddr, 100_000, db.WalletEntryCreditBack, 2)
	for _, oid := range []bson.ObjectID{walletPaid, withCheckout, reversed} {
		_, err = testDB.VotingProcess(oid)
		c.Assert(err, qt.Equals, db.ErrNotFound)
		_, err = testDB.ProcessPayment(oid)
		c.Assert(err, qt.Equals, db.ErrNotFound)
	}
}

// TestDeleteDraftAfterCutShortReversal: a failed publish's reversal that credited back and then
// stopped (before its payment row was refreshed) leaves the ledger settled: deleting the draft
// credits nothing again, and its payment row goes with it.
func TestDeleteDraftAfterCutShortReversal(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "deleteowed123456")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_delete_owed")
	fundWallet(t, integratorAddr, 100_000)
	draft := newManagedDraft(t, token, managedAddr)
	debitManagedDraft(c, draft)

	failMoneyStepOnce(t, moneyStepCache, draft)
	c.Assert(testAPI.reversePublishDebit(draft), qt.ErrorMatches, "simulated crash.*")
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(draft)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid) // the display cache lags

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", draft.Hex())
	assertWallet(t, integratorAddr, 100_000, db.WalletEntryCreditBack, 1)
	_, err = testDB.VotingProcess(draft)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = testDB.ProcessPayment(draft)
	c.Assert(err, qt.Equals, db.ErrNotFound)
}

// TestDeleteManagedOrgDropsPartiallyPublishedDraft: a draft some of whose elections mined cannot
// be deleted on its own (40904, so a publish can resume it), but a teardown drops it: its mined
// elections stay on chain like a published process's, nothing is credited back and its payment
// row stays as their audit trail. The mined question is stored CANCELED so the teardown's
// active-election guard does not read the chain for it.
func TestDeleteManagedOrgDropsPartiallyPublishedDraft(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "teardownpartial1")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_teardown_partial")
	fundWallet(t, integratorAddr, 100_000)
	draft := newManagedDraft(t, token, managedAddr)
	debitManagedDraft(c, draft)
	paidBalance := pricing.Cents(100_000 - integratorProcessCents)

	questions, err := testDB.QuestionsByProcess(draft)
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.Not(qt.HasLen), 0)
	mined := questions[0]
	mined.UpstreamID = internal.HexBytes(bytes.Repeat([]byte{0x0d}, 32))
	mined.Status = db.QuestionStatusCanceled
	_, err = testDB.SetQuestion(&mined)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.MarkVotingProcessMined(draft), qt.IsNil)

	requestAndAssertError(errors.ErrProcessPartiallyPublished, t, http.MethodDelete, token, nil,
		"processes", draft.Hex())
	_, err = testDB.VotingProcess(draft)
	c.Assert(err, qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil,
		"integrator", "organizations", managedAddr.String())
	_, err = testDB.VotingProcess(draft)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	_, err = testDB.Organization(managedAddr)
	c.Assert(err, qt.Equals, db.ErrNotFound)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, paidBalance)
	payment, err := testDB.ProcessPayment(draft)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
}

// TestManagedOrgTeardownBlocksDraftsAndPublish: while a managed organization is being torn down,
// no draft can be created or published in it (nothing created or debited after the teardown's
// draft sweep goes unrefunded in the bulk delete), and a second teardown is refused.
func TestManagedOrgTeardownBlocksDraftsAndPublish(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "teardownblock123")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_teardown_block")
	fundWallet(t, integratorAddr, 100_000)
	draft := newManagedDraft(t, token, managedAddr)

	testAPI.teardowns.Store(managedAddr, struct{}{})
	c.Cleanup(func() { testAPI.teardowns.Delete(managedAddr) })
	requestAndAssertError(errors.ErrDuplicateConflict, t, http.MethodPost, token,
		newVotingProcessRequest(managedAddr, postNewOrgMembers(t, token, managedAddr, 1)), processesCreateEndpoint)
	requestAndAssertError(errors.ErrDuplicateConflict, t, http.MethodPost, token, nil,
		"processes", draft.Hex(), "publish")
	requestAndAssertError(errors.ErrDuplicateConflict, t, http.MethodDelete, token, nil,
		"integrator", "organizations", managedAddr.String())
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	vp, err := testDB.VotingProcess(draft)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PublishInProgress(), qt.IsFalse)
}
