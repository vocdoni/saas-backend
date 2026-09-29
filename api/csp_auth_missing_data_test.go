package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestCSPAuthMissingData covers members missing required auth data at login. They are left out of
// the census, so they can never authenticate: a login leaving a required field empty is refused
// before the lookup (participants stored without it before this rule would hash like it), and any
// real value matches no participant.
func TestCSPAuthMissingData(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, adminToken, orgAddress,
		apicommon.OrgMember{MemberNumber: "M-001", Name: "Twin", NationalID: "", Email: ""},
		apicommon.OrgMember{MemberNumber: "M-002", Name: "Jane", NationalID: "DNI002", Email: "jane@example.com"},
	)
	c.Assert(members, qt.HasLen, 2)
	byNumber := make(map[string]string, len(members))
	for _, m := range members {
		byNumber[m.MemberNumber] = m.ID
	}
	twin, jane := byNumber["M-001"], byNumber["M-002"]

	// publishes a process over twin and jane: twin is left out of the census, missing the required data
	publishCensus := func(t *testing.T, authFields db.OrgMemberAuthFields, twoFaFields db.OrgMemberTwoFaFields) string {
		pid, _ := publishCensusProcess(t, adminToken, orgAddress, apicommon.CensusSpec{
			AuthFields: authFields, TwoFaFields: twoFaFields, MemberIDs: []string{twin, jane},
		}, 1)
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, adminToken, nil, "processes", pid)
		qt.Assert(t, got.Census.Size, qt.Equals, int64(1))
		return pid
	}

	t.Run("an empty auth field cannot be used to log in", func(t *testing.T) {
		c := qt.New(t)
		authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsNationalID}
		pid := publishCensus(t, authFields, db.OrgMemberTwoFaFields{})

		// before the fix this authenticated the member on the name alone
		for _, id := range []string{"", "   "} {
			err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{Name: "Twin", NationalID: id})
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		}
		err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{Name: "Twin", NationalID: "X1"})
		c.Assert(err.Code, qt.Equals, errors.ErrCensusParticipantNotFound.Code)

		// a complete member is unaffected
		postProcessAuth0(t, pid, &handlers.AuthRequest{Name: "Jane", NationalID: "DNI002"})
	})

	t.Run("a member without the 2FA channel cannot log in", func(t *testing.T) {
		c := qt.New(t)
		pid := publishCensus(t, db.OrgMemberAuthFields{}, db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail})

		err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{Email: "   "})
		c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		err = postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{Email: "twin@example.com"})
		c.Assert(err.Code, qt.Equals, errors.ErrCensusParticipantNotFound.Code)
	})
}
