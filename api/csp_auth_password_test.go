package api

import (
	"net/http"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestCSPAuthPassword covers voter login on a census that requires the member password. The other
// auth fields find the participant, then the password is checked against the member's stored hash
// before anything else about the member is looked at, and every way of getting it wrong answers
// exactly like an unknown participant.
func TestCSPAuthPassword(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, adminToken)

	alicePass, bobPass := testMemberPassword("alice-secret"), testMemberPassword("bob-secret")
	members := postOrgMembers(t, adminToken, orgAddress,
		apicommon.OrgMember{MemberNumber: "PW-1", Name: "Alice", Password: alicePass, Weight: "1"},
		apicommon.OrgMember{MemberNumber: "PW-2", Name: "Bob", Password: bobPass, Weight: "0"},
		apicommon.OrgMember{MemberNumber: "PW-3", Name: "Carol", Weight: "1"},
	)
	c.Assert(members, qt.HasLen, 3)

	pid, _ := publishCensusProcess(t, adminToken, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsPassword},
		Weighted:   true,
		MemberIDs:  memberIDs(members),
	}, 1)
	// Carol has no password, so she was left out of the census
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, adminToken, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(2))

	// auth0 sends the step 0 request and returns the raw answer, so answers can be compared byte for byte
	auth0 := func(t *testing.T, req *handlers.AuthRequest) (string, int) {
		body, code := testRequest(t, http.MethodPost, "", req, processAuthURL(pid, "0"))
		return string(body), code
	}
	unknownBody, unknownCode := auth0(t, &handlers.AuthRequest{MemberNumber: "PW-9", Password: alicePass})
	c.Assert(unknownCode, qt.Equals, errors.ErrCSPAuthFailed.HTTPstatus, qt.Commentf("response: %s", unknownBody))

	t.Run("the right password logs in", func(t *testing.T) {
		postProcessAuth0(t, pid, &handlers.AuthRequest{MemberNumber: "PW-1", Password: alicePass})
	})

	t.Run("a wrong password answers like an unknown participant", func(t *testing.T) {
		c := qt.New(t)
		for _, req := range []*handlers.AuthRequest{
			{MemberNumber: "PW-1", Password: bobPass},
			// zero weight would be a 40801, but the password is checked first
			{MemberNumber: "PW-2", Password: alicePass},
			// left out of the census for having no password
			{MemberNumber: "PW-3", Password: alicePass},
		} {
			body, code := auth0(t, req)
			c.Assert(code, qt.Equals, unknownCode, qt.Commentf("member %s: %s", req.MemberNumber, body))
			c.Assert(body, qt.Equals, unknownBody, qt.Commentf("member %s", req.MemberNumber))
		}
		// with the right password, the zero-weight member gets its own error
		err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{MemberNumber: "PW-2", Password: bobPass})
		c.Assert(err.Code, qt.Equals, errors.ErrZeroWeightVoter.Code)
	})

	t.Run("a missing password is missing auth data", func(t *testing.T) {
		c := qt.New(t)
		err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{MemberNumber: "PW-1"})
		c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		c.Assert(err, qt.ErrorMatches, ".*missing required auth data.*")
	})

	t.Run("a password that is not a client hash is refused", func(t *testing.T) {
		c := qt.New(t)
		for _, password := range []string{"alice-secret", strings.ToUpper(alicePass)} {
			err := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{MemberNumber: "PW-1", Password: password})
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
			c.Assert(err, qt.ErrorMatches, ".*invalid password format.*")
		}
	})

	t.Run("a census without the password ignores it", func(t *testing.T) {
		pid, _ := publishCensusProcess(t, adminToken, orgAddress, apicommon.CensusSpec{
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
			MemberIDs:  memberIDs(members),
		}, 1)
		postProcessAuth0(t, pid, &handlers.AuthRequest{MemberNumber: "PW-3", Password: "not-a-hash"})
	})
}
