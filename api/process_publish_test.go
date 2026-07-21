package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/types"
)

// TestPublishProcess publishes a draft (with electionParams) as an on-chain
// election against Voconed and asserts the election exists and that publishing is
// idempotent.
func TestPublishProcess(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "password123")

	// create an org with eager on-chain account provisioning (Phase 1)
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://pub-%d.com", internal.RandomInt(100000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	orgAddress := org.Address
	c.Assert(orgAddress, qt.Not(qt.Equals), common.Address{})

	// subscribe to a plan so NEW_PROCESS permission passes
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// seed a draft with election params (no census: on-chain census is the CSP)
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: orgAddress,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Test election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 100,
			Questions: []db.Question{{
				Title: db.MultiLangString{"default": "Question 1"},
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
					{Title: db.MultiLangString{"default": "No"}, Value: 1},
				},
			}},
			VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
			ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(draftID.IsZero(), qt.IsFalse)

	// publish: async — returns 202 + jobId, then poll until completed
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", job.Errors))
	c.Assert(job.Type, qt.Equals, db.JobTypePublishProcess)
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(len(job.Result.Address) > 0, qt.IsTrue)
	c.Assert(job.Result.Status, qt.Equals, "READY")

	// verify on chain
	client := testNewVocdoniClient(t)
	election, err := client.Election(types.HexBytes(job.Result.Address))
	c.Assert(err, qt.IsNil)
	c.Assert(election, qt.Not(qt.IsNil))

	// the info endpoint resolves the canonical metadata from its (https /storage)
	// reference — the Vochain itself only resolves ipfs pointers.
	info := requestAndParse[apicommon.ProcessInfo](t, http.MethodGet, token, nil, "process", draftID.Hex())
	c.Assert(info.MetadataURL, qt.Not(qt.Equals), "")
	c.Assert(info.Metadata, qt.Not(qt.HasLen), 0)
	c.Assert(info.Metadata["title"], qt.Not(qt.IsNil))

	// idempotency: once published the endpoint returns 200 with the same address (no tx)
	resp2 := requestAndParse[apicommon.PublishProcessResponse](
		t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(resp2.Address.String(), qt.Equals, job.Result.Address.String())
}

// TestJobStatusNotFound asserts an unknown job id returns 404.
func TestJobStatusNotFound(t *testing.T) {
	requestAndAssertError(errors.ErrJobNotFound, t, http.MethodGet, "", nil, "jobs", "deadbeefdeadbeef")
}
