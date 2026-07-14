package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

// testCreateProvisionedOrganization creates an organization with eager on-chain account
// provisioning, which is required before it can publish elections.
func testCreateProvisionedOrganization(t *testing.T, token string) common.Address {
	t.Helper()
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://vproc-%d.com", internal.RandomInt(1000000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	return org.Address
}

// TestVotingProcessPublish exercises the full batch-publish path end-to-end against the
// in-process chain: it creates a 2-question process and publishes it (one on-chain election
// per question, submitted via the node batch endpoint), asserting both questions get an
// on-chain id and become ready and the process is marked published.
func TestVotingProcessPublish(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// start the election immediately (no future start date)
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	// publish -> 202 { jobId }, poll to completion
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Error))
	c.Assert(job.Type, qt.Equals, db.JobTypePublishVotingProcess)

	// the process is now published with an on-chain election per question
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Published, qt.IsTrue)
	c.Assert(got.Questions, qt.HasLen, 2)
	for i := range got.Questions {
		c.Assert(len(got.Questions[i].UpstreamID) > 0, qt.IsTrue, qt.Commentf("question %d has no upstreamId", i))
	}

	// the single-question read reports the ready status
	q0 := requestAndParse[db.VotingProcessQuestion](
		t, http.MethodGet, "", nil, "processes", pid, "questions", got.Questions[0].ID.Hex())
	c.Assert(q0.Status, qt.Equals, db.QuestionStatusReady)

	// re-publishing a published process is an idempotent no-op (200, not a new job)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, nil, "processes", pid, "publish")
}
