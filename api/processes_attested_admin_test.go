package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// publishedAttestedProcess creates and publishes an attested process with the given signer and size.
func publishedAttestedProcess(
	t *testing.T, token string, orgAddress, signer common.Address, size int64,
	twoFa db.OrgMemberTwoFaFields,
) string {
	t.Helper()
	req := newAttestedProcessRequest(orgAddress, signer, size)
	req.Census.TwoFaFields = twoFa
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	qt.Assert(t, job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	return pid
}

// TestAttestedCensusSize: the declared size of a published attested census only grows, resizes the
// open questions on chain, and is refused for a draft and for a memberbase census; adding members
// to an attested census is refused.
func TestAttestedCensusSize(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedsize123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	signer := common.Address{0xaa}

	draft := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newAttestedProcessRequest(orgAddress, signer, 10), processesCreateEndpoint).ProcessID
	requestAndAssertCode(http.StatusConflict, t, http.MethodPut, token,
		&apicommon.AttestedCensusSizeRequest{Size: 20}, "processes", draft, "census", "size")

	pid := publishedAttestedProcess(t, token, orgAddress, signer, 10, nil)
	shrink := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.AttestedCensusSizeRequest{Size: 9}, "processes", pid, "census", "size")
	c.Assert(shrink.Code, qt.Equals, errors.ErrInvalidData.Code)

	job := enqueueAndPollJob(t, http.MethodPut, token,
		&apicommon.AttestedCensusSizeRequest{Size: 20}, "processes", pid, "census", "size")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("resize job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Attested.MaxCensusSize, qt.Equals, int64(20))

	members := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{"64b7f0e0a1b2c3d4e5f60718"}}, "processes", pid, "census")
	c.Assert(members.Code, qt.Equals, errors.ErrInvalidData.Code)

	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(1)...))
	plain, _ := publishedProcess(t, token, orgAddress, ids)
	notAttested := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.AttestedCensusSizeRequest{Size: 20}, "processes", plain, "census", "size")
	c.Assert(notAttested.Code, qt.Equals, errors.ErrInvalidData.Code)
}

// TestAttestationSignerRotation: rotation appends to the signer history, is idempotent for the active
// signer, and is refused for a zero signer and a memberbase census.
func TestAttestationSignerRotation(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedrotate1")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := publishedAttestedProcess(t, token, orgAddress, common.Address{0xaa}, 10, nil)

	zero := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.AttestationSignerRequest{}, "processes", pid, "attestation-signer")
	c.Assert(zero.Code, qt.Equals, errors.ErrInvalidData.Code)

	next := common.Address{0xbb}
	for range 2 {
		got := requestAndParse[apicommon.AttestedCensusSpec](t, http.MethodPut, token,
			&apicommon.AttestationSignerRequest{Signer: next}, "processes", pid, "attestation-signer")
		c.Assert(got.Signer, qt.Equals, next)
		c.Assert(got.SignerHistory, qt.HasLen, 2)
		c.Assert(got.VerifyingContract, qt.Not(qt.IsNil))
	}

	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(1)...))
	plain, _ := publishedProcess(t, token, orgAddress, ids)
	notAttested := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.AttestationSignerRequest{Signer: next}, "processes", plain, "attestation-signer")
	c.Assert(notAttested.Code, qt.Equals, errors.ErrInvalidData.Code)
}
