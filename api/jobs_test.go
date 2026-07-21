package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestJobStatusImportErrorsGatedByRole verifies that member-import error strings — which can embed
// member PII (emails/phones/birthdates of the failing rows) — are returned by GET /jobs/{jobId}
// only to an authenticated manager/admin of the job's org, and stripped for anonymous callers and
// non-members. The same job's detail also stays available on the auth-gated GET /jobs list.
func TestJobStatusImportErrorsGatedByRole(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "jobspiiadminpass123")
	orgAddress := testCreateOrganization(t, adminToken)
	// a second user with no role on the org
	strangerToken := testCreateUser(t, "jobspiistrangerpass123")

	// seed a completed member-import job whose error string embeds member PII.
	jobID, err := apicommon.NewJobID()
	c.Assert(err, qt.IsNil)
	const piiEmail = "victim@secret.example"
	const piiPhone = "+34600123456"
	importErr := "could not parse email: " + piiEmail + " invalid; invalid phone \"" + piiPhone + "\""
	c.Assert(testDB.CreateJob(jobID, db.JobTypeOrgMembers, orgAddress, 3), qt.IsNil)
	c.Assert(testDB.CompleteJob(jobID, 2, []string{importErr}), qt.IsNil)

	// stripped: raw body carries no PII, errors empty, but status + counters remain.
	assertStripped := func(who, token string) {
		body, code := testRequest(t, http.MethodGet, token, nil, "jobs", jobID)
		c.Assert(code, qt.Equals, http.StatusOK)
		c.Assert(strings.Contains(string(body), piiEmail), qt.IsFalse, qt.Commentf("%s body leaked email: %s", who, body))
		c.Assert(strings.Contains(string(body), piiPhone), qt.IsFalse, qt.Commentf("%s body leaked phone: %s", who, body))
		var resp apicommon.JobResponse
		c.Assert(json.Unmarshal(body, &resp), qt.IsNil)
		c.Assert(resp.Errors, qt.HasLen, 0, qt.Commentf("%s got error detail", who))
		c.Assert(resp.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(resp.Result, qt.Not(qt.IsNil))
		c.Assert(resp.Result.Total, qt.Equals, 3)
		c.Assert(resp.Result.Added, qt.Equals, 2)
	}

	// (1) anonymous and (3) a non-member both get the stripped response.
	assertStripped("anonymous", "")
	assertStripped("stranger", strangerToken)

	// (4) an EXPIRED admin token must not unlock the detail either (temporal validation gates it).
	// Mint one for the admin's email, mirroring buildLoginResponse but with a past expiry.
	valid, err := jwtauth.VerifyToken(testAPI.auth, adminToken)
	c.Assert(err, qt.IsNil)
	emailClaim, ok := valid.Get("userId")
	c.Assert(ok, qt.IsTrue)
	j := jwt.New()
	c.Assert(j.Set("userId", emailClaim), qt.IsNil)
	c.Assert(j.Set(jwt.ExpirationKey, time.Now().Add(-time.Hour)), qt.IsNil)
	jmap, err := j.AsMap(context.Background())
	c.Assert(err, qt.IsNil)
	_, expiredToken, err := testAPI.auth.Encode(jmap)
	c.Assert(err, qt.IsNil)
	assertStripped("expired-admin", expiredToken)

	// (2) the org admin sees the full per-row error detail on GET /jobs/{jobId}.
	adminJob := requestAndParse[apicommon.JobResponse](t, http.MethodGet, adminToken, nil, "jobs", jobID)
	c.Assert(adminJob.Errors, qt.Contains, importErr)

	// the auth-gated GET /jobs list keeps the full detail too.
	list := requestAndParse[apicommon.JobsListResponse](
		t, http.MethodGet, adminToken, nil, "jobs?orgAddress="+orgAddress.String())
	var found *apicommon.JobResponse
	for i := range list.Jobs {
		if list.Jobs[i].JobID == jobID {
			found = &list.Jobs[i]
			break
		}
	}
	c.Assert(found, qt.Not(qt.IsNil), qt.Commentf("seeded job not found in the admin list"))
	c.Assert(found.Errors, qt.Contains, importErr)
}
