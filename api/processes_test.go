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
)

// newVotingProcessRequest builds a 2-question (singlechoice + multichoice) create request
// with the given census member ids and an eligibility subset on the second question.
func newVotingProcessRequest(
	orgAddress common.Address, memberIDs []string,
) *apicommon.CreateVotingProcessRequest {
	yesNo := []db.Choice{
		{Title: db.MultiLangString{"default": "Yes"}, Value: 0}, //nolint:goconst
		{Title: db.MultiLangString{"default": "No"}, Value: 1},
	}
	return &apicommon.CreateVotingProcessRequest{
		OrgAddress: orgAddress,
		Census: apicommon.CensusSpec{
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
			MemberIDs:   memberIDs,
		},
		Title:     db.MultiLangString{"default": "Test process"},
		StartDate: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		EndDate:   time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		Questions: []apicommon.VotingProcessQuestionRequest{
			{
				Title:     db.MultiLangString{"default": "Q1"},
				Choices:   yesNo,
				Type:      db.VotingTypeSingleChoice,
				TypeSetup: db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1},
			},
			{
				Title:       db.MultiLangString{"default": "Q2"},
				Choices:     yesNo,
				Type:        db.VotingTypeMultiChoice,
				TypeSetup:   db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 2},
				Eligibility: &apicommon.EligibilitySpec{MemberIDs: memberIDs[:1]}, // only the first member
			},
		},
	}
}

func TestVotingProcessAuthoring(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// create a 2-question draft
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	c.Assert(created.ProcessID, qt.Not(qt.Equals), "")
	pid := created.ProcessID

	// read it back (full, hydrated questions)
	got := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(got.ID, qt.Equals, pid)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Questions, qt.HasLen, 2)
	c.Assert(got.Questions[0].Type, qt.Equals, db.VotingTypeSingleChoice)
	c.Assert(got.Questions[1].Type, qt.Equals, db.VotingTypeMultiChoice)
	// eligibility subset is public and restricted to the first member on Q2
	c.Assert(got.Questions[1].EligibleMemberIDs, qt.HasLen, 1)
	c.Assert(got.Questions[0].EligibleMemberIDs, qt.HasLen, 0) // Q1 = all census members
	// census config is exposed, member list is not
	c.Assert(got.Census.TwoFaFields, qt.HasLen, 1)

	// list contains the process with its questions
	list := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, adminToken, nil, fmt.Sprintf("processes?orgAddress=%s&limit=100", orgAddress.Hex()))
	c.Assert(len(list.Processes) >= 1, qt.IsTrue)
	found := false
	for _, p := range list.Processes {
		if p.ID == pid {
			found = true
			c.Assert(p.Questions, qt.HasLen, 2)
		}
	}
	c.Assert(found, qt.IsTrue)

	// validate (dry-run): a complete draft is ready to publish
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "check")
	c.Assert(validation.Valid, qt.IsTrue, qt.Commentf("errors: %v", validation.Errors))

	// the public single-question read is voter-facing: a draft (unpublished) process is not
	// readable without auth (404), so draft content and eligibility are not exposed.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil,
		"processes", pid, "questions", got.Questions[0].ID.Hex())
}

func TestVotingProcessUpdate(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	pid := created.ProcessID

	// update the title while still a draft
	upd := newVotingProcessRequest(orgAddress, ids)
	upd.Title = db.MultiLangString{"default": "Updated title"}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, upd, "processes", pid)

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(got.Title["default"], qt.Equals, "Updated title")
}

func TestVotingProcessAuthoringErrors(t *testing.T) {
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// zero questions -> 400
	empty := newVotingProcessRequest(orgAddress, ids)
	empty.Questions = nil
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, empty, processesCreateEndpoint)

	// eligibility member not in the census -> 400
	badElig := newVotingProcessRequest(orgAddress, ids)
	badElig.Questions[1].Eligibility = &apicommon.EligibilitySpec{MemberIDs: []string{"000000000000000000000000"}}
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, badElig, processesCreateEndpoint)

	// a multichoice question with an out-of-range maxChoices -> 400 (0 is unbounded, >choices
	// is nonsensical). Question index 1 is the multichoice one with 2 choices.
	zeroMax := newVotingProcessRequest(orgAddress, ids)
	zeroMax.Questions[1].TypeSetup.MaxChoices = 0
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, zeroMax, processesCreateEndpoint)

	tooManyMax := newVotingProcessRequest(orgAddress, ids)
	tooManyMax.Questions[1].TypeSetup.MaxChoices = 5 // only 2 choices
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, tooManyMax, processesCreateEndpoint)

	// a question with neither a type nor a ballotProtocol -> 400
	noShape := newVotingProcessRequest(orgAddress, ids)
	noShape.Questions[0].Type = ""
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, noShape, processesCreateEndpoint)

	// a question with an unsupported type (and no ballotProtocol) -> 400
	badType := newVotingProcessRequest(orgAddress, ids)
	badType.Questions[0].Type = "quadratic"
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPost, adminToken, badType, processesCreateEndpoint)

	// every create above failed, so no orphaned draft was left behind (they roll back)
	count, err := testDB.CountVotingProcesses(orgAddress, db.AllProcesses)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, count, qt.Equals, int64(0))

	// a raw ballotProtocol override satisfies the ballot-shape requirement even without a type
	rawProto := newVotingProcessRequest(orgAddress, ids)
	rawProto.Questions[0].Type = ""
	rawProto.Questions[0].BallotProtocol = &db.BallotProtocol{MaxCount: 1, MaxValue: 1}
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken, rawProto, processesCreateEndpoint)

	// a user with no role on the org -> 401
	otherToken := testCreateUser(t, "otherpassword123")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, otherToken,
		newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

	// unauthenticated read of a process -> 401
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, "", nil, "processes", created.ProcessID)
}
