package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// holdPaidSubmittedDraft debits a funded draft and records a submitted publish tx at nonce 3 on
// its first question, as a publish worker that then died would leave it, releasing the claim.
func holdPaidSubmittedDraft(c *qt.C, oid bson.ObjectID) {
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testAPI.debitProcessWallet(vp, org, nil), qt.IsNil)
	c.Assert(testDB.ClearVotingProcessPublishing(oid, owner), qt.IsNil)
	holdPublishTxs(c, oid, db.PublishTx{
		Hash: randomHash(), Nonce: 3, MetadataURL: "https://saas.test/storage/q.json", SubmittedAt: time.Now(),
	})
}

// TestUnresolvedPublishTxBlocksMoney (AC4): while the chain cannot answer, a submitted publish tx
// is unresolved: no credit-back, and the draft's edit and delete answer 409 (40903). Once the
// chain is back and proves the tx dead, the delete goes ahead and refunds the draft.
func TestUnresolvedPublishTxBlocksMoney(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "publishunresolved")
	integratorAddr, pid, oid := newFundedManagedDraft(t, token, "prod_test_publish_unresolved")
	holdPaidSubmittedDraft(c, oid)
	chainDown := fmt.Errorf("chain unavailable")
	useFakePublishChain(t, &fakePublishChain{nonceErr: chainDown, lookupErr: chainDown})

	owner, claimed, err := testDB.ClaimVotingProcessForPublish(oid, time.Time{})
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	c.Assert(testAPI.reversePublishDebit(oid), qt.IsNil)
	c.Assert(testDB.ClearVotingProcessPublishing(oid, owner), qt.IsNil)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)

	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	members := getOrgMembers(t, token, vp.OrgAddress)
	edit := newVotingProcessRequest(vp.OrgAddress, memberIDs(members.Members))
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodPut, token, edit, "processes", pid)
	requestAndAssertError(errors.ErrPublishInProgress, t, http.MethodDelete, token, nil, "processes", pid)

	// the chain is back: the nonce moved past the record's and its hash never mined
	useFakePublishChain(t, &fakePublishChain{nonce: 4})
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
}

// TestDeleteOrgDraftsRefusesSubmittedDraft (AC12): cmd/userdel's DeleteOrgDrafts has no chain
// client, so a draft with a submitted publish tx stops it with 409 (40903) and nothing is erased
// or credited.
func TestDeleteOrgDraftsRefusesSubmittedDraft(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "userdelsubmitted")
	integratorAddr, _, oid := newFundedManagedDraft(t, token, "prod_test_userdel_submitted")
	holdPaidSubmittedDraft(c, oid)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)

	err = DeleteOrgDrafts(testDB, vp.OrgAddress)
	c.Assert(err, qt.ErrorIs, errors.ErrPublishInProgress)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))
}

// TestEditAfterDeadPublishTx (AC13): an edit of a draft whose only submitted tx is dead resolves
// it first, then succeeds and leaves the draft with no submitted tx.
func TestEditAfterDeadPublishTx(t *testing.T) {
	c := qt.New(t)
	token, orgAddress, oid := newTwoQuestionDraft(t, "editdeadpublish1")
	holdPublishTxs(c, oid, db.PublishTx{
		Hash: randomHash(), Nonce: 3, MetadataURL: "https://saas.test/storage/q.json", SubmittedAt: time.Now(),
	})
	useFakePublishChain(t, &fakePublishChain{nonce: 4})

	members := getOrgMembers(t, token, orgAddress)
	edit := newVotingProcessRequest(orgAddress, memberIDs(members.Members))
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, edit, "processes", oid.Hex())
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PublishSubmitted, qt.IsFalse)
	for i := range questions {
		c.Assert(questions[i].PublishTxs, qt.HasLen, 0)
	}
}
