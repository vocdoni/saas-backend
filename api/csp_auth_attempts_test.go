package api

import (
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestCSPAuthAttemptBudget covers the hourly failed step-0 budgets. They are keyed by what a
// request submits, so a participant and a non-participant run out of them identically (the
// refusal cannot tell who is in the census). A wrong contact only spends its own submission's
// budget and, more slowly, the claimed voter's, so whoever knows a voter's login fields cannot
// lock them out with a few junk contacts. There is no process-wide budget that junk attempts
// could spend to lock every other voter out.
func TestCSPAuthAttemptBudget(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, adminToken)

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
	// ... and run out of budget alike, so the refusal does not reveal census membership either.
	// The refusal says how long until the hour ends
	refused := postProcessAuth0AndExpectError(t, pid, alice("wrong@example.com"))
	c.Assert(refused.Code, qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code,
		qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	data, ok := refused.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", refused.Data))
	retryAfter, ok := data["retryAfter"].(float64)
	c.Assert(ok, qt.IsTrue, qt.Commentf("error data: %#v", refused.Data))
	c.Assert(retryAfter >= 1 && retryAfter <= 3600, qt.IsTrue, qt.Commentf("retryAfter: %v", retryAfter))

	// the voter's own submission keeps its budget: junk contacts did not lock Alice out
	postProcessAuth0(t, pid, alice("alice.budget@example.com"))

	// guessing many different contacts for one voter is bounded by the coarser voter budget...
	voterBudget := csp.DefaultMaxHourlyAuthFailures * csp.VoterAuthFailuresFactor
	for i := csp.DefaultMaxHourlyAuthFailures; i < voterBudget; i++ {
		c.Assert(postProcessAuth0AndExpectError(t, pid, alice(fmt.Sprintf("guess-%d@example.com", i))).Code,
			qt.Equals, errors.ErrCSPAuthFailed.Code)
	}
	c.Assert(postProcessAuth0AndExpectError(t, pid, alice("another-guess@example.com")).Code,
		qt.Equals, errors.ErrVerificationMaxAttempts.Code)
	// ... which, once spent, refuses the voter until the hour ends even with the right contact
	c.Assert(postProcessAuth0AndExpectError(t, pid, alice("alice.budget@example.com")).Code,
		qt.Equals, errors.ErrVerificationMaxAttempts.Code)

	// every other voter of the process is unaffected by all those failures
	postProcessAuth0(t, pid, &handlers.AuthRequest{MemberNumber: "B-002", Name: "Bob", Email: "bob.budget@example.com"})
}
