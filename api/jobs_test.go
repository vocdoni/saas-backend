package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

// TestJobStatusImportErrorsNotPublic verifies that member-import error strings — which can embed
// member PII (emails/phones/birthdates of the failing rows) — are NOT returned by the public
// GET /jobs/{jobId} endpoint, while the same job's full error detail stays available on the
// auth-gated GET /jobs list for a Manager/Admin of the org.
func TestJobStatusImportErrorsNotPublic(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "jobspiiadminpass123")
	orgAddress := testCreateOrganization(t, adminToken)

	// seed a completed member-import job whose error string embeds member PII.
	jobID, err := apicommon.NewJobID()
	c.Assert(err, qt.IsNil)
	const piiEmail = "victim@secret.example"
	const piiPhone = "+34600123456"
	importErr := "could not parse email: " + piiEmail + " invalid; invalid phone \"" + piiPhone + "\""
	c.Assert(testDB.CreateJob(jobID, db.JobTypeOrgMembers, orgAddress, 3), qt.IsNil)
	c.Assert(testDB.CompleteJob(jobID, 2, []string{importErr}), qt.IsNil)

	// public GET /jobs/{jobId}: no error detail, no PII in the raw body; status + counters remain.
	body, code := testRequest(t, http.MethodGet, "", nil, "jobs", jobID)
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(strings.Contains(string(body), piiEmail), qt.IsFalse, qt.Commentf("public body leaked email: %s", body))
	c.Assert(strings.Contains(string(body), piiPhone), qt.IsFalse, qt.Commentf("public body leaked phone: %s", body))
	var pub apicommon.JobResponse
	c.Assert(json.Unmarshal(body, &pub), qt.IsNil)
	c.Assert(pub.Errors, qt.HasLen, 0)
	c.Assert(pub.Status, qt.Equals, db.JobStatusCompleted)
	c.Assert(pub.Result, qt.Not(qt.IsNil))
	c.Assert(pub.Result.Total, qt.Equals, 3)
	c.Assert(pub.Result.Added, qt.Equals, 2)

	// auth-gated GET /jobs list (Manager/Admin): the same job keeps its full error detail.
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
