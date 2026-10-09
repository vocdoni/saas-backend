package api

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestCSPAuthAttemptBudget covers the hourly failed step-0 budget. It is keyed by the login fields
// a request submits, so a participant and a non-participant run out of it identically (the
// refusal cannot tell who is in the census), and there is no process-wide budget that junk
// attempts could spend to lock every other voter out.
func TestCSPAuthAttemptBudget(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, adminToken, orgAddress,
		apicommon.OrgMember{MemberNumber: "B-001", Name: "Alice", Email: "alice.budget@example.com"},
		apicommon.OrgMember{MemberNumber: "B-002", Name: "Bob", Email: "bob.budget@example.com"},
	)
	c.Assert(members, qt.HasLen, 2)
	memberIDs := []string{members[0].ID, members[1].ID}
	pid, _ := publishCensusProcess(t, adminToken, orgAddress, apicommon.CensusSpec{
		AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName},
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs,
	}, 1)

	alice := func(email string) *handlers.AuthRequest {
		return &handlers.AuthRequest{MemberNumber: "B-001", Name: "Alice", Email: email}
	}
	nobody := &handlers.AuthRequest{MemberNumber: "B-999", Name: "Nobody", Email: "nobody@example.com"}

	// a participant guessed with the wrong contact, and an identity matching nobody, fail alike...
	for range csp.DefaultMaxHourlyAuthFailures {
		c.Assert(postProcessAuth0AndExpectError(t, pid, alice("wrong@example.com")).Code,
			qt.Equals, errors.ErrCSPAuthFailed.Code)
		c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code,
			qt.Equals, errors.ErrCSPAuthFailed.Code)
	}
	// ... and run out of budget alike, so the refusal does not reveal census membership either
	c.Assert(postProcessAuth0AndExpectError(t, pid, alice("wrong@example.com")).Code,
		qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code,
		qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	// the spent identity is refused until the hour ends even with the right contact: this is what
	// bounds guessing a voter's contact, at the price of letting whoever knows their login fields
	// lock them out for the rest of the hour. The refusal says how long that is
	refused := postProcessAuth0AndExpectError(t, pid, alice("alice.budget@example.com"))
	c.Assert(refused.Code, qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	data, ok := refused.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", refused.Data))
	retryAfter, ok := data["retryAfter"].(float64)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", refused.Data))
	c.Assert(retryAfter >= 1 && retryAfter <= 3600, qt.IsTrue, qt.Commentf("retryAfter: %v", retryAfter))

	// every other voter of the process is unaffected by all those failures
	postProcessAuth0(t, pid, &handlers.AuthRequest{MemberNumber: "B-002", Name: "Bob", Email: "bob.budget@example.com"})
}
