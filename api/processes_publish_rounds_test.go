package api

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestPublishResubmitsAtSameNonceAfterLostSubmit (AC8): a round whose batch submit fails with a
// transport error after its txs were recorded, on a chain where nothing mined, is followed by a
// round that resubmits the same questions at the same nonces: one election per question, and the
// organization's nonce moves by exactly one tx per question.
func TestPublishResubmitsAtSameNonceAfterLostSubmit(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "publishlostsubmit")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)), processesCreateEndpoint)
	oid := objectID(c, created.ProcessID)
	startNonce, err := testAPI.account.AccountNonce(orgAddress)
	c.Assert(err, qt.IsNil)

	var submits atomic.Int32
	testAPI.publishSubmitFault = func() error {
		if submits.Add(1) == 1 {
			return fmt.Errorf("read tcp: connection reset by peer")
		}
		return nil
	}
	t.Cleanup(func() { testAPI.publishSubmitFault = nil })

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	c.Assert(submits.Load(), qt.Equals, int32(2))
	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Published, qt.IsTrue)
	c.Assert(vp.PublishSubmitted, qt.IsFalse)
	for i := range questions {
		c.Assert(questions[i].UpstreamID, qt.Not(qt.HasLen), 0)
		c.Assert(questions[i].PublishTxs, qt.HasLen, 0)
	}
	nonce, err := testAPI.account.AccountNonce(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(nonce, qt.Equals, startNonce+uint32(len(questions)))
	elections, err := testAPI.account.OrgElectionIDs(orgAddress, 0)
	c.Assert(err, qt.IsNil)
	c.Assert(elections, qt.HasLen, len(questions))
}
