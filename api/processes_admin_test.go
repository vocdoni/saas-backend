package api

import (
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestVotingProcessDelete verifies a draft can be deleted by a manager (and is then gone), that a
// non-manager cannot, and that a published process cannot be deleted.
func TestVotingProcessDelete(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	pid := created.ProcessID

	// a user with no role for the org cannot delete the draft
	other := testCreateUser(t, "otherpass123")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodDelete, other, nil, "processes", pid)

	// the manager deletes the draft; it is then gone
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, token, nil, "processes", pid)

	// a published process cannot be deleted (409). Seed one directly as published.
	vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
		OrgAddress: orgAddress, Published: true, Title: db.MultiLangString{"default": "published"}, //nolint:goconst
	})
	c.Assert(err, qt.IsNil)
	requestAndAssertCode(http.StatusConflict, t, http.MethodDelete, token, nil, "processes", vpID.Hex())
}

func TestWithDraftID(t *testing.T) {
	c := qt.New(t)
	typed := withDraftID(errors.ErrPublishInProgress, "abc")
	var apiErr errors.Error
	c.Assert(stderrors.As(typed, &apiErr), qt.IsTrue)
	c.Assert(apiErr.Code, qt.Equals, errors.ErrPublishInProgress.Code)
	c.Assert(apiErr.HTTPstatus, qt.Equals, http.StatusConflict)
	c.Assert(typed.Error(), qt.Contains, "draft abc")

	rec := httptest.NewRecorder()
	writeAPIError(rec, typed)
	c.Assert(rec.Code, qt.Equals, http.StatusConflict)
	c.Assert(rec.Body.String(), qt.Contains, "draft abc")

	plain := withDraftID(stderrors.New("boom"), "abc")
	rec = httptest.NewRecorder()
	writeAPIError(rec, plain)
	c.Assert(rec.Code, qt.Equals, http.StatusInternalServerError)
	c.Assert(rec.Body.String(), qt.Contains, "draft abc")
	c.Assert(rec.Body.String(), qt.Not(qt.Contains), "boom")
}
