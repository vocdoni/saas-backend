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
	"github.com/vocdoni/saas-backend/errors"
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
func newFundedManagedDraft(t *testing.T, token, key string) (common.Address, string, bson.ObjectID) {
	t.Helper()
	c := qt.New(t)
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token)
	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)),
		processesCreateEndpoint)
	oid, err := bson.ObjectIDFromHex(created.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_" + key,
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
	c.Assert(ledgerAttempt(c, integratorAddr, oid), qt.Equals, 1)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3)
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
	c.Assert(entries[0].AmountCents, qt.Equals, pricing.Cents(integratorProcessCents))
	c.Assert(entries[0].IdempotencyKey, qt.Equals, "publishfail:"+oid.Hex()+":0")
	c.Assert(entries[0].ProcessOrgAddress, qt.Equals, payment.OrgAddress)
	// the credit-back frees what the managed organization had spent under its cap
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.ManagedOrgSpend(payment.OrgAddress).SpentCents, qt.Equals, pricing.Cents(0))
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
	c.Assert(ledgerAttempt(c, integratorAddr, oid), qt.Equals, 2)

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

// ledgerAttempt is the process's attempt as its payer's ledger folds it: the number of failed
// publishes credited back.
func ledgerAttempt(c *qt.C, payer common.Address, oid bson.ObjectID) int {
	state, err := testDB.ProcessLedgerState(payer, oid)
	c.Assert(err, qt.IsNil)
	return state.Attempt
}

// TestPublishAfterCutShortReversal: a reversal that credited back and stopped before refreshing
// the payment row leaves the ledger settled and the attempt moved on, whatever the row says; the
// next publish pays again under the next attempt, so the wallet ends up charged exactly once.
func TestPublishAfterCutShortReversal(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishowed12345")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_owed")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)

	failMoneyStepOnce(t, moneyStepCache, oid)
	c.Assert(testAPI.reversePublishDebit(oid), qt.ErrorMatches, "simulated crash.*")
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	c.Assert(ledgerAttempt(c, integratorAddr, oid), qt.Equals, 1)

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
	_, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)
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

// TestWalletChargeAfterReversal: a wallet charge after a failed publish was credited back (a
// census headroom purchase racing the reversal, or the next publish) finds the draft unpaid on the
// ledger and charges the whole price again, under the next attempt's key.
func TestWalletChargeAfterReversal(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishowedcharge")
	integratorAddr, _, oid := newFundedManagedDraft(t, token, "prod_test_publish_owed_charge")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)
	c.Assert(testAPI.reversePublishDebit(oid), qt.IsNil)

	// a headroom charge priced from what the draft paid before the reversal is refused
	paidBefore := pricing.Cents(integratorProcessCents)
	_, err = testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: integratorProcessCents + 500}, fromCents: &paidBefore,
	})
	c.Assert(err, qt.ErrorIs, errors.ErrPaymentSessionConflict)

	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	debited, err := testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: quote, addOns: pricedOrgAddOns(&input.QuoteInput),
	})
	c.Assert(err, qt.IsNil)
	c.Assert(debited, qt.Equals, pricing.Cents(integratorProcessCents))
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, fmt.Sprintf("%s:1:%d", oid.Hex(), integratorProcessCents))
}

// TestPublishFinalFailureCreditsBackStandard: a standard organization pays from its own wallet,
// and a publish that fails for good credits that wallet back, with a creditBack history row.
func TestPublishFinalFailureCreditsBackStandard(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishcreditstd")
	orgAddress := testCreateProvisionedOrganization(t, token)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	fundWallet(t, orgAddress, 100_000)

	injectPublishFaults(t, func(int32) error { return fmt.Errorf("draft cannot be built") })
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	pollPaymentStatus(t, pid, db.ProcessPaymentFailed)
	assertWallet(t, orgAddress, 100_000, db.WalletEntryCreditBack, 1)
	c.Assert(ledgerAttempt(c, orgAddress, oid), qt.Equals, 1)
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
	// a checkout opened after the credit-back and then cancelled drops its payment row; the attempt
	// is read off the ledger, so the publish that follows still charges again under a new key
	openProcessTopUp(t, fake, token, pid)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid, "checkout")
	_, err = testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, paidAgain := openProcessTopUp(t, fake, token, pid)
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paidAgain), qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, pid)
	// the first attempt, the drain, the new attempt; the cancelled checkout charged nothing
	assertWallet(t, orgAddress, 0, db.WalletEntryPayment, 3)
}

// TestTopUpWebhookReplayReadsTheLedgerNotTheRow: a top-up published its process, the publish
// failed for good and was credited back, but the payment row's display still says paid (its
// refresh was lost). A redelivered event must read the credit-back off the ledger: no debit, no
// publish, the credit stays in the wallet.
func TestTopUpWebhookReplayReadsTheLedgerNotTheRow(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)
	injectPublishFaults(t, func(int32) error { return fmt.Errorf("permanent fault") })

	token := testCreateUser(t, "replaystalerow12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid := objectID(c, pid)
	_, paid := openProcessTopUp(t, fake, token, pid)
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paid), qt.Equals, http.StatusOK)
	pollPaymentStatus(t, pid, db.ProcessPaymentFailed)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryCreditBack, 1)
	c.Assert(testDB.SetProcessPaymentCache(db.SetProcessPaymentCacheParams{
		ProcessID: oid, OrgAddress: orgAddress, Status: db.ProcessPaymentPaid, AmountCents: pricedProcessCents,
	}), qt.IsNil)

	testAPI.publishFault = nil
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8),
		"checkout.session.completed", paid), qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 1)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
}

// TestTopUpWebhookFailureAfterDebitIsRefusal: a top-up whose publish fails after the wallet paid
// and before the job exists is credited back, recorded as the payment's refusal and answered 200;
// a replay debits nothing and publishes nothing.
func TestTopUpWebhookFailureAfterDebitIsRefusal(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)
	testAPI.publishPreJobFault = func() error { return fmt.Errorf("census could not be published") }
	t.Cleanup(func() { testAPI.publishPreJobFault = nil })
	token := testCreateUser(t, "afterdebitrefusal")
	orgAddress := testCreateProvisionedOrganization(t, token)
	pid := newPricedVotingProcess(t, token, orgAddress)
	_, paid := openProcessTopUp(t, fake, token, pid)

	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid),
		qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 1)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryCreditBack, 1)
	status := requestAndParse[apicommon.ProcessPaymentStatusResponse](
		t, http.MethodGet, token, nil, "processes", pid, "checkout")
	c.Assert(status.Status, qt.Equals, db.ProcessPaymentFailed)
	c.Assert(status.Refusal, qt.Not(qt.IsNil))
	c.Assert(status.Refusal.Code, qt.Equals, errors.ErrGenericInternalServerError.Code)
	c.Assert(status.Refusal.Message, qt.Not(qt.Contains), "census could not be published") // server detail stays in the log

	testAPI.publishPreJobFault = nil
	c.Assert(postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid),
		qt.Equals, http.StatusOK)
	assertWallet(t, orgAddress, pricedProcessCents, db.WalletEntryPayment, 1)
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsFalse)
}

// TestPublishFailureAfterMiningKeepsPayment: a publish whose final failure comes after its
// elections mined keeps the debit — they exist and were paid for — and publishing again resumes
// them for free.
func TestPublishFailureAfterMiningKeepsPayment(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishminedfail")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_mined_fail")
	testAPI.publishMinedFault = func() error { return fmt.Errorf("permanent failure after mining") }
	t.Cleanup(func() { testAPI.publishMinedFault = nil })

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.HasMinedElections, qt.IsTrue)
	c.Assert(vp.Published, qt.IsFalse)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	testAPI.publishMinedFault = nil
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	pollProcessPublished(t, token, pid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 2) // the top-up and the one debit: no credit-back
}

// TestStaleSweepTakesOverRetryingPublish: the stale sweep takes over the claim of a publish
// waiting to retry, reverts it and credits it back; the worker's retry then finds its claim lost
// and stops without a second credit-back.
func TestStaleSweepTakesOverRetryingPublish(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "staleretrypublish")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_stale_retry")
	injectPublishFaults(t, func(attempt int32) error {
		if attempt == 1 {
			return transient(fmt.Errorf("chain unavailable"))
		}
		// the retry starts after a restart's sweep found its claim stale
		vp, err := testDB.VotingProcess(oid)
		if err != nil {
			return err
		}
		vp.Publishing = time.Now().Add(-db.PublishStaleAfter - time.Minute)
		if _, err := testDB.SetVotingProcess(vp); err != nil {
			return err
		}
		testAPI.reconcileStalePublishing()
		return nil
	})

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)
	c.Assert(ledgerAttempt(c, integratorAddr, oid), qt.Equals, 1)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 3) // top-up, debit, one credit-back
	c.Assert(entries[0].Kind, qt.Equals, db.WalletEntryCreditBack)
}

// retryWaitingOnOrgTxLock creates a standard organization's priced draft on the auto "All members"
// group (one whole-census question) and starts its publish with a first attempt that fails
// transiently. The test takes the organization's tx lock as that attempt releases it, so the retry
// waits for it.
func retryWaitingOnOrgTxLock(t *testing.T, name string) retryWaiting {
	t.Helper()
	c := qt.New(t)
	token := testCreateUser(t, name)
	orgAddress := testCreateProvisionedOrganization(t, token)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(growthBaseVoters)...))
	autoGroup, err := testDB.AutoMemberGroup(orgAddress)
	c.Assert(err, qt.IsNil)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Questions = req.Questions[:1]
	req.Census = apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		GroupID:    autoGroup.ID.Hex(),
	}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	fundWallet(t, orgAddress, growthCents(t, growthBaseVoters))

	held := make(chan *orgTxLock, 1)
	injectPublishFaults(t, func(attempt int32) error {
		if attempt == 1 {
			// queued on the lock while this attempt holds it: it gets it before the retry does
			go func() { held <- testAPI.orgTxLocks.lock(orgAddress) }()
			return transient(fmt.Errorf("chain unavailable"))
		}
		return nil
	})
	publishRetryBackoff = 500 * time.Millisecond
	enq := requestAndParseWithAssertCode[apicommon.EnqueuedResponse](
		http.StatusAccepted, t, http.MethodPost, token, nil, "processes", pid, "publish")
	return retryWaiting{token: token, org: orgAddress, vp: vp, jobID: enq.JobID, lock: <-held}
}

// retryWaiting is a publish whose retry waits for the organization's tx lock the test holds.
type retryWaiting struct {
	token string
	org   common.Address
	vp    *db.VotingProcess
	jobID string
	lock  *orgTxLock
}

// TestRetryingPublishKeepsItsClaimFresh: a publish whose retry waits for the organization's tx lock
// longer than PublishStaleAfter keeps its claim fresh meanwhile, so a member change on its census
// still answers 409 and the retry publishes the census it stored.
func TestRetryingPublishKeepsItsClaimFresh(t *testing.T) {
	c := qt.New(t)
	restore := db.PublishStaleAfter
	db.PublishStaleAfter = 3 * time.Second
	t.Cleanup(func() { db.PublishStaleAfter = restore })
	rw := retryWaitingOnOrgTxLock(t, "retryclaimfresh1")
	token, orgAddress, vp, jobID, lock := rw.token, rw.org, rw.vp, rw.jobID, rw.lock

	time.Sleep(2 * db.PublishStaleAfter)
	stored, err := testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.PublishInProgress(), qt.IsTrue)
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodPost, token,
		&apicommon.AddMembersRequest{Members: newOrgMembers(1)}, organizationMembersURL(orgAddress.String()))

	lock.Unlock()
	job := pollJob(t, jobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", vp.ID.Hex())
	c.Assert(got.Census.Size, qt.Equals, int64(growthBaseVoters))
	elec, err := testAPI.account.Election(got.Questions[0].UpstreamID)
	c.Assert(err, qt.IsNil)
	c.Assert(elec.Census.MaxCensusSize, qt.Equals, uint64(growthBaseVoters))
}

// TestRetryingPublishStandsDownOnStaleClaim: a retry whose claim went stale anyway (here written
// stale directly, so no renewal can refresh it) does not mine from the census it stored, which
// growth was free to change meanwhile: the worker stands down, and the stale sweep resets the
// draft, credits it back and clears the claim.
func TestRetryingPublishStandsDownOnStaleClaim(t *testing.T) {
	c := qt.New(t)
	rw := retryWaitingOnOrgTxLock(t, "retryclaimstale1")
	token, orgAddress, vp, jobID, lock := rw.token, rw.org, rw.vp, rw.jobID, rw.lock

	stored, err := testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	stored.Publishing = time.Now().Add(-db.PublishStaleAfter - time.Minute)
	_, err = testDB.SetVotingProcess(stored)
	c.Assert(err, qt.IsNil)
	// no longer guarded: the census grows
	postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(census.Size, qt.Equals, int64(growthBaseVoters+1))

	lock.Unlock()
	job := pollJob(t, jobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	questions, err := testDB.QuestionsByProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	for i := range questions {
		c.Assert(questions[i].UpstreamID, qt.HasLen, 0)
	}
	stored, err = testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Published, qt.IsFalse)
	c.Assert(stored.Publishing.IsZero(), qt.IsTrue)
	c.Assert(walletBalance(c, orgAddress), qt.Equals, growthCents(t, growthBaseVoters))
	payment, err := testDB.ProcessPayment(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)
}

// TestPublishStandsDownWithoutCreditAfterSubmitting: an attempt whose claim goes stale once it has
// submitted a batch stands down without crediting anything back, since what it submitted may be on
// chain: the debit stays, no question is reset and the stale claim is left to the stale sweep.
func TestPublishStandsDownWithoutCreditAfterSubmitting(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "submittedstale12")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_submitted_stale")
	testAPI.publishSubmittedHook = func() {
		vp, err := testDB.VotingProcess(oid)
		if err == nil {
			vp.Publishing = time.Now().Add(-db.PublishStaleAfter - time.Minute)
			_, err = testDB.SetVotingProcess(vp)
		}
		if err != nil {
			t.Errorf("could not make the claim stale: %v", err)
		}
	}
	t.Cleanup(func() { testAPI.publishSubmittedHook = nil })

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(job.Errors, qt.Contains, errPublishClaimLost.Error())
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 2) // top-up, debit: no credit-back
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Not(qt.Equals), db.ProcessPaymentFailed)
	// the stale claim is still there, unreconciled (a reconcile would have reclaimed and cleared
	// it, resetting the questions on the way): the stale sweep takes it from here
	claims, err := testDB.StaleVotingProcesses()
	c.Assert(err, qt.IsNil)
	found := false
	for _, claim := range claims {
		found = found || claim.ID == oid
	}
	c.Assert(found, qt.IsTrue)
}
