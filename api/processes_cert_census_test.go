package api

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// testCertGatewayKeyID and testCertGatewaySeed are the shared signature test vector key (seed bytes
// 0x00..0x1f): csp.TestCertGatewaySignatureVectors and cert-gateway's tests use it too.
const testCertGatewayKeyID = "test-1"

var testCertGatewayKey = ed25519.NewKeyFromSeed([]byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
})

// setCertGatewayKeys sets the certificate gateway keys of the shared test CSP for the rest of the
// test; nil turns certificate auth off. It mutates shared testAPI state, like setDisableBlindCSP.
func setCertGatewayKeys(t *testing.T, keys csp.CertGatewayKeys) {
	t.Helper()
	previous := testAPI.csp.CertGatewayKeys()
	testAPI.csp.SetCertGatewayKeys(keys)
	t.Cleanup(func() { testAPI.csp.SetCertGatewayKeys(previous) })
}

// enableCertAuth turns certificate auth on with the test vector key.
func enableCertAuth(t *testing.T) {
	t.Helper()
	setCertGatewayKeys(t, csp.CertGatewayKeys{
		testCertGatewayKeyID: testCertGatewayKey.Public().(ed25519.PublicKey),
	})
}

// testDNI returns a valid DNI (8 digits and their check letter) for n.
func testDNI(n int) string {
	return fmt.Sprintf("%08d%c", n, "TRWAGMYFPDXBNJZSQVHLCKE"[n%23])
}

// certCensus is a certificate census spec over the given members.
func certCensus(twoFa db.OrgMemberTwoFaFields, memberIDs ...string) apicommon.CensusSpec {
	return apicommon.CensusSpec{
		AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		TwoFaFields: twoFa,
		CertAuth:    true,
		MemberIDs:   memberIDs,
	}
}

// TestCertCensusValidation (AC3): a certificate census needs authFields exactly ["nationalId"] and
// the feature on; any 2FA subset works; members with an invalid DNI, or without the 2FA channel,
// are reported as missing data and left out of the census; certAuth is echoed on reads.
func TestCertCensusValidation(t *testing.T) {
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "C-1", NationalID: testDNI(1), Email: "c1@example.com", Phone: "+34600000001"},
		apicommon.OrgMember{MemberNumber: "C-2", NationalID: "1234567-l", Email: "c2@example.com", Phone: "+34600000002"},
		apicommon.OrgMember{MemberNumber: "C-3", NationalID: "12345678A", Email: "c3@example.com", Phone: "+34600000003"},
		apicommon.OrgMember{MemberNumber: "C-4", NationalID: testDNI(4), Phone: "+34600000004"},
	)
	ids := memberIDs(members)
	valid, invalidDNI, noEmail := ids[:2], ids[2], ids[3]

	validate := func(spec apicommon.CensusSpec) ([]byte, int) {
		return testRequest(t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec},
			processesCensusValidateEndpoint)
	}

	t.Run("feature off", func(t *testing.T) {
		setCertGatewayKeys(t, nil)
		requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(nil, valid...)},
			processesCensusValidateEndpoint)
		req := minimalVotingProcessRequest(orgAddress)
		req.Census = certCensus(nil, valid...)
		requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token, req, processesCreateEndpoint)
	})

	enableCertAuth(t)

	t.Run("authFields other than [nationalId]", func(t *testing.T) {
		c := qt.New(t)
		for _, auth := range []db.OrgMemberAuthFields{
			nil,
			{db.OrgMemberAuthFieldsMemberNumber},
			{db.OrgMemberAuthFieldsNationalID, db.OrgMemberAuthFieldsName},
		} {
			spec := certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, valid...)
			spec.AuthFields = auth
			body, code := validate(spec)
			c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("auth=%v: %s", auth, body))
			c.Assert(string(body), qt.Contains, fmt.Sprintf(`"code":%d`, errors.ErrInvalidData.Code))
			req := minimalVotingProcessRequest(orgAddress)
			req.Census = spec
			requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token, req, processesCreateEndpoint)
		}
	})

	t.Run("every 2FA subset", func(t *testing.T) {
		c := qt.New(t)
		for _, twoFa := range []db.OrgMemberTwoFaFields{
			nil,
			{db.OrgMemberTwoFaFieldEmail},
			{db.OrgMemberTwoFaFieldPhone},
			{db.OrgMemberTwoFaFieldEmail, db.OrgMemberTwoFaFieldPhone},
		} {
			body, code := validate(certCensus(twoFa, valid...))
			c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("twoFa=%v: %s", twoFa, body))
		}
	})

	t.Run("invalid DNI and missing 2FA channel are missing data", func(t *testing.T) {
		c := qt.New(t)
		apiErr := requestAndExpectError(t, http.MethodPost, token, &apicommon.ValidateProcessCensusRequest{
			OrgAddress: orgAddress.Bytes(),
			Census:     certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...),
		}, processesCensusValidateEndpoint)
		c.Assert(apiErr.Code, qt.Equals, errors.ErrInvalidData.Code)
		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue, qt.Commentf("data: %v", apiErr.Data))
		c.Assert(data["missingData"], qt.ContentEquals, []any{invalidDNI, noEmail})

		req := minimalVotingProcessRequest(orgAddress)
		req.Census = certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...)
		created := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
			processesCreateEndpoint)
		c.Assert(created.MissingData, qt.ContentEquals, []string{invalidDNI, noEmail})
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
		c.Assert(got.Census.CertAuth, qt.IsTrue)
		c.Assert(got.Census.Size, qt.Equals, int64(len(valid)))
	})
}

// TestCertCensusPublishNeedsFeature: a draft with a certificate census created while the feature was
// on cannot be published once the gateway keys are gone, and publishes again when they are back.
func TestCertCensusPublishNeedsFeature(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, apicommon.OrgMember{MemberNumber: "P-1", NationalID: testDNI(1)}))
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = certCensus(nil, ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	payIfPriced(t, token, pid)

	t.Run("feature off", func(t *testing.T) {
		setCertGatewayKeys(t, nil)
		requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token, nil, "processes", pid, "publish")
	})

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
}

// TestCertCensusPublishRefusalWins: with certificate auth off, a cert draft that the organization's
// publish check also refuses (here, over the duration cap) answers 40025, not that later error.
func TestCertCensusPublishRefusalWins(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, apicommon.OrgMember{MemberNumber: "W-1", NationalID: testDNI(51)}))
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = certCensus(nil, ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	// over the duration cap, stored as drafts were before they were capped
	_, err := testDB.DBClient.Database(testDBName).Collection("votingProcesses").UpdateOne(context.Background(),
		bson.M{"_id": objectID(c, pid)}, bson.M{"$set": bson.M{"endDate": time.Now().Add(120 * 24 * time.Hour).UTC()}})
	c.Assert(err, qt.IsNil)

	setCertGatewayKeys(t, nil)
	requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token, nil, "processes", pid, "publish")
}
