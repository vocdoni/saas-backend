package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// certCensus is a certificate census over the given members.
func certCensus(memberIDs ...string) apicommon.CensusSpec {
	return apicommon.CensusSpec{
		AuthMethod: db.CensusAuthMethodCertificate,
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		MemberIDs:  memberIDs,
	}
}

// certVoters creates an organization with one member per nationalId, as stored (weights 1, 2...),
// and returns the admin token, the organization and the member ids in order.
func certVoters(t *testing.T, nationalIDs ...string) (string, common.Address, []string) {
	t.Helper()
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := make([]apicommon.OrgMember, len(nationalIDs))
	for i, nid := range nationalIDs {
		members[i] = apicommon.OrgMember{
			MemberNumber: fmt.Sprintf("C%d-%d", internal.RandomInt(1000000), i), NationalID: nid,
			Weight: strconv.Itoa(i + 1),
		}
	}
	stored := postOrgMembers(t, token, orgAddress, members...)
	byNumber := make(map[string]string, len(stored))
	for _, m := range stored {
		byNumber[m.MemberNumber] = m.ID
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = byNumber[m.MemberNumber]
	}
	return token, orgAddress, ids
}

// TestCertAuthDisabledCensus pins the census half of feature off (AC1): without certificate sign-in
// configured, a certificate census is refused on create and validation with ErrCertificateAuthDisabled,
// before the db layer (which knows nothing of the feature) sees it.
func TestCertAuthDisabledCensus(t *testing.T) {
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress)
	req.Census = certCensus(ids...)
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, token, req, processesCreateEndpoint)
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)
}

// TestCertificateCensusSpec pins the census authMethod round trip (AC2): accepted on create, draft
// update and validation with authFields ["nationalId"] and no 2FA, echoed on the public process read
// and the list; any other field combination and unknown methods answer 400.
func TestCertificateCensusSpec(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")

	req := minimalVotingProcessRequest(orgAddress)
	req.Census = certCensus(ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	c.Assert(got.Census.AuthFields, qt.DeepEquals, db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID})

	// a draft update keeps it, and can drop it again
	req.Census.Anonymous = true
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, req, "processes", pid)
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	c.Assert(got.Census.Anonymous, qt.IsTrue)

	// published, the public read and the list echo it too
	pid, _ = publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	public := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", pid)
	c.Assert(public.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	list := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil,
		processesCreateEndpoint+"?orgAddress="+orgAddress.String())
	found := false
	for _, p := range list.Processes {
		if p.ID == pid {
			found = true
			c.Assert(p.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
		}
	}
	c.Assert(found, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)

	for name, census := range map[string]apicommon.CensusSpec{
		"with 2fa": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		},
		"other field": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		},
		"extra field": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID, db.OrgMemberAuthFieldsName},
		},
		"unknown method": {
			AuthMethod: "fingerprint", MemberIDs: ids, AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		},
	} {
		bad := minimalVotingProcessRequest(orgAddress)
		bad.Census = census
		c.Assert(requestAndExpectError(t, http.MethodPost, token, bad, processesCreateEndpoint).Code,
			qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("%s", name))
		c.Assert(requestAndExpectError(t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: census},
			processesCensusValidateEndpoint).Code, qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("%s", name))
	}
}

// TestValidateCertificateCensus pins the validation of a certificate census (AC3): members whose
// nationalId is missing, has a wrong letter, is another tax id or is not a DNI/NIE are warnings only
// (200, never missingData); canonical duplicates still answer 400 naming both members.
func TestValidateCertificateCensus(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z", "", "12345678A", "B12345678", "passport-1")

	resp := requestAndParse[apicommon.ValidateProcessCensusResponse](t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)
	classes := map[string]string{}
	for _, w := range resp.NationalIDWarnings {
		classes[w.MemberID] = w.Class
	}
	c.Assert(classes, qt.DeepEquals, map[string]string{
		ids[1]: "missing", ids[2]: "letter_mismatch", ids[3]: "unsupported_document", ids[4]: "invalid",
	})

	// the same members on a typed nationalId census: the missing one blocks, as before
	_, status := testRequest(t, http.MethodPost, token, &apicommon.ValidateProcessCensusRequest{
		OrgAddress: orgAddress.Bytes(),
		Census:     apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: ids},
	}, processesCensusValidateEndpoint)
	c.Assert(status, qt.Equals, http.StatusBadRequest)

	// canonical twins block, naming both, with the duplicate warnings
	token2, org2, twinIDs := certVoters(t, "12345678Z", "12.345.678-z")
	body, status := testRequest(t, http.MethodPost, token2,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: org2.Bytes(), Census: certCensus(twinIDs...)},
		processesCensusValidateEndpoint)
	c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", body))
	c.Assert(string(body), qt.Contains, twinIDs[0])
	c.Assert(string(body), qt.Contains, twinIDs[1])
	c.Assert(string(body), qt.Contains, "duplicate_after_normalization")
}

// TestCertificateCensusPublishGuard pins the publish guard (AC5): a certificate census does not
// publish on a ledger without SP1's nationalId migration (409), and publishes once it ran.
func TestCertificateCensusPublishGuard(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = certCensus(ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	payIfPriced(t, token, pid)

	ledger := testDB.DBClient.Database(testDBName).Collection("migrations")
	version := migrations.NationalIDNormalizationVersion
	_, err := ledger.DeleteMany(context.Background(), bson.M{"version": bson.M{"$gte": version}})
	c.Assert(err, qt.IsNil)
	failure := requestAndExpectError(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(failure.Code, qt.Equals, errors.ErrDuplicateConflict.Code)
	c.Assert(failure.Error(), qt.Contains, "nationalId hash migration pending")

	c.Assert(testDB.RunMigrationsUp(), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// a certificate draft on a deployment without the feature cannot publish either
	draft := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	payIfPriced(t, token, draft)
	testPort = testAPI.port // the feature-off API; useCertAPI's cleanup restores it after the test
	failure = requestAndExpectError(t, http.MethodPost, token, nil, "processes", draft, "publish")
	c.Assert(failure.Code, qt.Equals, errors.ErrCertificateAuthDisabled.Code)
}

// TestAuthZeroCertificateCensus pins that a certificate census never signs in with typed details
// (AC5): auth/0 answers the certificate envelope, reason certificate_census.
func TestAuthZeroCertificateCensus(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	failure := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{NationalID: "12345678Z"})
	c.Assert(failure.Code, qt.Equals, errors.ErrCertificateAuth.Code)
	data, ok := failure.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(data["reason"], qt.Equals, "certificate_census")
	retryable, isBool := data["retryable"].(bool)
	c.Assert(isBool, qt.IsTrue)
	c.Assert(retryable, qt.IsFalse)
}
