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

// newAttestedProcessRequest builds a create request for an attested census with no 2FA and no
// question eligibility, starting immediately.
func newAttestedProcessRequest(
	orgAddress, signer common.Address, maxCensusSize int64,
) *apicommon.CreateVotingProcessRequest {
	// the member id only feeds the eligibility this request drops below
	req := newVotingProcessRequest(orgAddress, []string{"64b7f0e0a1b2c3d4e5f60718"})
	req.StartDate = ""
	req.Census = apicommon.CensusSpec{
		Attested: &apicommon.AttestedCensusSpec{Signer: signer, MaxCensusSize: maxCensusSize},
	}
	for i := range req.Questions {
		req.Questions[i].Eligibility = nil
	}
	return req
}

// TestAttestedProcessCreateAndPublish: an attested census is created without members, echoes its
// signer, history, maxCensusSize and the environment's verifying contract, and publishes with the
// declared size.
func TestAttestedProcessCreateAndPublish(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedpass123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	signer := common.Address{0xaa}

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newAttestedProcessRequest(orgAddress, signer, 12), processesCreateEndpoint)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
	c.Assert(got.Census.Attested, qt.Not(qt.IsNil))
	c.Assert(got.Census.Attested.Signer, qt.Equals, signer)
	c.Assert(got.Census.Attested.MaxCensusSize, qt.Equals, int64(12))
	c.Assert(got.Census.Attested.SignerHistory, qt.HasLen, 1)
	domain, err := testCSP.AttestationDomain()
	c.Assert(err, qt.IsNil)
	c.Assert(got.Census.Attested.VerifyingContract, qt.Not(qt.IsNil))
	c.Assert(*got.Census.Attested.VerifyingContract, qt.Equals, domain.VerifyingContract)
	c.Assert(got.Census.Size, qt.Equals, int64(12))

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	published := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
	c.Assert(published.Published, qt.IsTrue)
	c.Assert(published.Census.Size, qt.Equals, int64(12))
}

// TestAttestedProcessRefusesMemberOptions: every member-based option, and a 2FA other than one
// channel, is refused with 400 40037 at create.
func TestAttestedProcessRefusesMemberOptions(t *testing.T) {
	token := testCreateUser(t, "attestedpass456")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	signer := common.Address{0xaa}

	cases := map[string]func(*apicommon.CreateVotingProcessRequest){
		"no signer": func(r *apicommon.CreateVotingProcessRequest) { r.Census.Attested.Signer = common.Address{} },
		"zero size": func(r *apicommon.CreateVotingProcessRequest) { r.Census.Attested.MaxCensusSize = 0 },
		"group":     func(r *apicommon.CreateVotingProcessRequest) { r.Census.GroupID = "64b7f0e0a1b2c3d4e5f60718" },
		"member ids": func(r *apicommon.CreateVotingProcessRequest) {
			r.Census.MemberIDs = []string{"64b7f0e0a1b2c3d4e5f60718"}
		},
		"auth fields": func(r *apicommon.CreateVotingProcessRequest) { r.Census.AuthFields = db.OrgMemberAuthFields{"name"} },
		"weighted":    func(r *apicommon.CreateVotingProcessRequest) { r.Census.Weighted = true },
		"anonymous":   func(r *apicommon.CreateVotingProcessRequest) { r.Census.Anonymous = true },
		"two channels": func(r *apicommon.CreateVotingProcessRequest) {
			r.Census.TwoFaFields = db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail, db.OrgMemberTwoFaFieldPhone}
		},
		"eligibility": func(r *apicommon.CreateVotingProcessRequest) {
			r.Questions[1].Eligibility = &apicommon.EligibilitySpec{MemberIDs: []string{"64b7f0e0a1b2c3d4e5f60718"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := newAttestedProcessRequest(orgAddress, signer, 10)
			mutate(req)
			resp := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest,
				t, http.MethodPost, token, req, processesCreateEndpoint)
			qt.Assert(t, resp.Code, qt.Equals, errors.ErrInvalidData.Code)
		})
	}

	// a single 2FA channel is allowed
	req := newAttestedProcessRequest(orgAddress, signer, 10)
	req.Census.TwoFaFields = db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldPhone}
	requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req, processesCreateEndpoint)
}
