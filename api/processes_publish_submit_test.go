package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// waitPublishTxsMined waits for the recorded publish tx of every question of a process to mine
// and returns the election id the node predicted for each question (or the one already adopted).
// It runs inside publish fault hooks, off the test goroutine, so it returns errors instead of
// asserting.
func waitPublishTxsMined(oid bson.ObjectID) (map[bson.ObjectID]internal.HexBytes, error) {
	questions, err := testDB.QuestionsByProcess(oid)
	if err != nil {
		return nil, err
	}
	predicted := make(map[bson.ObjectID]internal.HexBytes, len(questions))
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) > 0 {
			predicted[q.ID] = q.UpstreamID
			continue
		}
		if len(q.PublishTxs) != 1 {
			return nil, fmt.Errorf("question %s holds %d publish txs", q.ID.Hex(), len(q.PublishTxs))
		}
		if err := testAPI.account.WaitTxMined(q.PublishTxs[0].Hash); err != nil {
			return nil, err
		}
		predicted[q.ID] = q.PublishTxs[0].UpstreamID
	}
	return predicted, nil
}

// assertAdopted asserts that the process's questions record exactly the predicted elections, that
// the process is flagged mined with nothing left submitted, and that its organization has no
// other election on chain.
func assertAdopted(c *qt.C, oid bson.ObjectID, predicted map[bson.ObjectID]internal.HexBytes) {
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.HasMinedElections, qt.IsTrue)
	c.Assert(vp.PublishSubmitted, qt.IsFalse)
	c.Assert(questions, qt.HasLen, len(predicted))
	for i := range questions {
		c.Assert(questions[i].UpstreamID.String(), qt.Equals, predicted[questions[i].ID].String())
		c.Assert(questions[i].PublishTxs, qt.HasLen, 0)
	}
	elections, err := testAPI.account.OrgElectionIDs(vp.OrgAddress, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(elections, qt.HasLen, len(questions))
}

// TestPublishFailureAfterSubmitKeepsPayment (AC1): a publish that fails for good right after its
// batch was submitted credits nothing back, since its elections may exist; once they mined, a
// republish adopts them, with no second debit and no second NEW_PROCESS tx.
func TestPublishFailureAfterSubmitKeepsPayment(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishsubmitfail")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_submit_fail")
	testAPI.publishSubmittedFault = func() error { return fmt.Errorf("permanent failure after submit") }
	t.Cleanup(func() { testAPI.publishSubmittedFault = nil })

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	predicted, err := waitPublishTxsMined(oid)
	c.Assert(err, qt.IsNil)

	testAPI.publishSubmittedFault = nil
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	pollProcessPublished(t, token, pid)
	assertAdopted(c, oid, predicted)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 2) // the top-up and the one debit: no credit-back
}

// TestStaleSweepAdoptsSubmittedElection (AC2): the service dies right after a submit whose txs
// then mine; the stale sweep adopts the elections instead of crediting the publish back, and a
// republish completes without minting new ones.
func TestStaleSweepAdoptsSubmittedElection(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishsweepadopt")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_sweep_adopt")
	var predicted map[bson.ObjectID]internal.HexBytes
	testAPI.publishSubmittedFault = func() error {
		var err error
		if predicted, err = waitPublishTxsMined(oid); err != nil {
			return err
		}
		// the worker is gone: its claim goes stale and the sweep takes the process over
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
	}
	t.Cleanup(func() { testAPI.publishSubmittedFault = nil })

	// the worker finds its claim lost after the hook and stops without abandoning
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	assertAdopted(c, oid, predicted)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	testAPI.publishSubmittedFault = nil
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	pollProcessPublished(t, token, pid)
	assertAdopted(c, oid, predicted)
	_, entries, err := testDB.WalletLedger(integratorAddr, 1, 10)
	c.Assert(err, qt.IsNil)
	c.Assert(entries, qt.HasLen, 2)
}

// TestStaleSweepCreditsBackDeadPublishTx (AC3): a submitted tx whose nonce another tx consumed,
// with its own hash never mined, is dead: the sweep releases it and credits the publish back
// exactly as for a publish that submitted nothing.
func TestStaleSweepCreditsBackDeadPublishTx(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishdeadtx123")
	integratorAddr, _, oid := newFundedManagedDraft(t, token, "prod_test_publish_dead_tx")
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPublishSubmitted(oid, owner), qt.IsNil)
	questions, err := testDB.QuestionsByProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.AppendQuestionPublishTx(oid, questions[0].ID, db.PublishTx{
		Hash: randomHash(), Nonce: 3, MetadataURL: "https://saas.test/storage/q.json", SubmittedAt: time.Now(),
	}), qt.IsNil)
	useFakePublishChain(t, &fakePublishChain{nonce: 4})

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
	vp, questions, err = testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PublishSubmitted, qt.IsFalse)
	c.Assert(vp.HasMinedElections, qt.IsFalse)
	c.Assert(questions[0].PublishTxs, qt.HasLen, 0)
}
