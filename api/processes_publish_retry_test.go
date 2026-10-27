package api

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// injectPublishFaults makes every publish attempt run fail(attempt) first (attempts count from 1)
// and shortens the retry backoff, restoring both when the test ends. It returns the attempt counter.
func injectPublishFaults(t *testing.T, fail func(attempt int32) error) *atomic.Int32 {
	t.Helper()
	var attempts atomic.Int32
	backoff := publishRetryBackoff
	publishRetryBackoff = 10 * time.Millisecond
	testAPI.publishFault = func() error { return fail(attempts.Add(1)) }
	t.Cleanup(func() {
		testAPI.publishFault = nil
		publishRetryBackoff = backoff
	})
	return &attempts
}

// newFundedManagedDraft creates a managed organization with a priced draft (email 2FA,
// integratorProcessCents) and an integrator wallet holding 100 000 cents, returning the integrator
// and the draft.
func newFundedManagedDraft(t *testing.T, token, planID string) (common.Address, string, bson.ObjectID) {
	t.Helper()
	c := qt.New(t)
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, planID)
	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_" + planID,
	}), qt.IsNil)
	return integratorAddr, created.ProcessID, oid
}

// walletBalance reads a wallet's balance.
func walletBalance(c *qt.C, integrator common.Address) pricing.Cents {
	wallet, err := testDB.Wallet(integrator)
	c.Assert(err, qt.IsNil)
	return wallet.BalanceCents
}

// TestPublishRetriesTransientFailure: the service retries a transient publish failure itself, the
// job staying pending, and the wallet is debited once however many attempts it takes.
func TestPublishRetriesTransientFailure(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishretry1234")
	integratorAddr, pid, _ := newFundedManagedDraft(t, token, "prod_test_publish_retry")
	attempts := injectPublishFaults(t, func(attempt int32) error {
		if attempt <= 2 {
			return transient(fmt.Errorf("chain unavailable"))
		}
		return nil
	})

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	c.Assert(attempts.Load(), qt.Equals, int32(3))
	pollProcessPublished(t, token, pid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	total, _, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(2)) // top-up + one debit
}

// TestPublishFinalFailureCreditsBack: a publish whose retries run out, or that fails for a
// permanent reason, credits the payer's wallet back with a creditBack history row and leaves an
// unpaid draft; publishing it again charges again.
func TestPublishFinalFailureCreditsBack(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishcredit123")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_credit")

	// transient every time: retried publishRetryAttempts times, then abandoned
	attempts := injectPublishFaults(t, func(int32) error { return transient(fmt.Errorf("chain unavailable")) })
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(attempts.Load(), qt.Equals, int32(publishRetryAttempts+1))
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(integratorProcessCents))
	c.Assert(entries[0].IdempotencyKey, qt.Equals, "publishfail:"+oid.Hex()+":0")
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, payment.OrgAddress)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PublishInProgress(), qt.IsFalse)

	// a permanent failure is not retried, and is credited back the same way
	attempts.Store(0)
	testAPI.publishFault = func() error {
		attempts.Add(1)
		return fmt.Errorf("draft cannot be built")
	}
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(attempts.Load(), qt.Equals, int32(1))
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.WalletAttempt, qt.Equals, 2)

	// publishing again after the credit-backs charges again, once
	testAPI.publishFault = nil
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	_, entries, err = testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 6)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, fmt.Sprintf("%s:2:%d", oid.Hex(), integratorProcessCents))
}

// TestPublishCreditBackAppliedAfterLostCredit: a reversal that reverted the payment but died
// before crediting leaves the payer owed; the next publish applies that credit before it
// debits again, so the wallet ends up charged exactly once.
func TestPublishCreditBackAppliedAfterLostCredit(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishowed12345")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_owed")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)

	// the reversal's first write, without its credit
	reverted, err := testDB.RevertWalletProcessPayment(db.WalletPaymentRevert{
		ProcessID: oid, AmountCents: integratorProcessCents, CreditBackCents: integratorProcessCents,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(reverted, qt.IsTrue)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
}

// TestStalePublishReconcileCreditsBack: a publish that died with the service (its marker left
// stale) is a final failure too, and the startup sweep credits the payer's wallet back.
func TestStalePublishReconcileCreditsBack(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishstale1234")
	integratorAddr, _, oid := newFundedManagedDraft(t, token, "prod_test_publish_stale")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	claimed, err := testDB.ClaimVotingProcessForPublish(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	// the marker the dead worker left, already older than the stale threshold
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	vp.Publishing = time.Now().Add(-db.PublishStaleAfter - time.Minute)
	_, err = testDB.SetVotingProcess(vp)
	c.Assert(err, qt.IsNil)
	testAPI.reconcileStalePublishing()

	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)
	vp, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Publishing.IsZero(), qt.IsTrue)
}

// TestWalletChargeAppliesOwedCreditBack: a wallet charge that records over a reverted payment
// still owing its credit-back (a census headroom purchase racing a failed publish) applies that
// credit first, since the paid record it writes drops what the failed one owed.
func TestWalletChargeAppliesOwedCreditBack(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishowedcharge")
	integratorAddr, _, oid := newFundedManagedDraft(t, token, "prod_test_publish_owed_charge")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	reverted, err := testDB.RevertWalletProcessPayment(db.WalletPaymentRevert{
		ProcessID: oid, AmountCents: integratorProcessCents, CreditBackCents: integratorProcessCents,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(reverted, qt.IsTrue)

	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	debited, err := testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: quote, branding: input.Branding, customURL: input.CustomURL,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(debited, qt.Equals, pricing.Cents(integratorProcessCents))
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
}

// TestPublishFinalFailureCreditsBackStandard: a standard organization pays from its own wallet,
// and a publish that fails for good credits that wallet back, with a creditBack history row.
func TestPublishFinalFailureCreditsBackStandard(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishcreditstd")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	fundWallet(t, orgAddress, 100_000)

	injectPublishFaults(t, func(int32) error { return fmt.Errorf("draft cannot be built") })
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	pollPaymentStatus(t, pid, db.ProcessPaymentFailed)
	assertWallet(t, orgAddress, 100_000, db.WalletEntryCreditBack, 1)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Payer, qt.Equals, orgAddress)
	c.Assert(payment.WalletAttempt, qt.Equals, 1)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, "publishfail:"+oid.Hex()+":0")
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, orgAddress)

	// publishing again charges again, under the next attempt's key
	testAPI.publishFault = nil
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	pollProcessPublished(t, token, pid)
	assertWallet(t, orgAddress, 100_000-pricedProcessCents, db.WalletEntryPayment, 2)
}

// pollPaymentStatus waits for the process payment to reach want (a worker's credit-back lands
// after the webhook answered).
func pollPaymentStatus(t *testing.T, pid string, want db.ProcessPaymentStatus) {
	t.Helper()
	c := qt.New(t)
	oid := objectID(c, pid)
	for range 200 {
		if payment, err := testDB.ProcessPayment(oid); err == nil && payment.Status == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.Fatalf("payment of process %s never reached %s", pid, want)
}

// TestTopUpWebhookReplayAfterFailedPublishDoesNotDebit: a top-up published its process, the
// publish failed for good and was credited back (failed, attempt moved on). Stripe resends the
// event: no new credit, no debit under the new attempt's key, no publish.
func TestTopUpWebhookReplayAfterFailedPublishDoesNotDebit(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)
	injectPublishFaults(t, func(int32) error { return fmt.Errorf("permanent fault") })

	token := testCreateUser(t, "replayfailed1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	_, paid := openProcessTopUp(t, fake, token, pid)
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paid), qt.Equals, http.StatusOK)
	pollPaymentStatus(t, pid, db.ProcessPaymentFailed) // the worker gave up and credited back
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryCreditBack, 1)

	testAPI.publishFault = nil
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paid), qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryTopUp, 1)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 1)
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)

	// the credited-back money is spent elsewhere; a top-up for this draft then publishes it and
	// debits again under the new attempt — the old attempt's debit key must not cover it
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: bson.NewObjectID(), ProcessOrgAddress: orgAddress,
		AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)
	// a checkout opened after the credit-back and then cancelled must not forget the attempt:
	// the publish that follows charges again under a new key
	openProcessTopUp(t, fake, token, pid)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid, "checkout")
	pollPaymentStatus(t, pid, db.ProcessPaymentFailed)
	_, paidAgain := openProcessTopUp(t, fake, token, pid)
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paidAgain), qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
	// the first attempt, the drain, the new attempt; the cancelled checkout charged nothing
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 3)
}
