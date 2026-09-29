package api

import (
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
	orgAddress := testCreateOrganization(t, adminToken)

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

	// publishes a census of twin and jane: twin is left out, missing the required data
	publishCensus := func(t *testing.T, authFields db.OrgMemberAuthFields, twoFaFields db.OrgMemberTwoFaFields) string {
		censusID := postCensus(t, adminToken, orgAddress, authFields, twoFaFields)
		group := postGroup(t, adminToken, orgAddress, twin, jane)
		census := postGroupCensus(t, adminToken, censusID, group.ID,
			&apicommon.PublishCensusGroupRequest{AuthFields: authFields, TwoFaFields: twoFaFields})
		qt.Assert(t, census.Size, qt.Equals, int64(1))
		return censusID
	}

	t.Run("an empty auth field cannot be used to log in", func(t *testing.T) {
		c := qt.New(t)
		authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsNationalID}
		censusID := publishCensus(t, authFields, db.OrgMemberTwoFaFields{})
		bundleID, _ := postProcessBundle(t, adminToken, censusID, randomProcessID())

		// before the fix this authenticated the member on the name alone
		for _, id := range []string{"", "   "} {
			err := postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{Name: "Twin", NationalID: id})
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		}
		err := postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{Name: "Twin", NationalID: "X1"})
		c.Assert(err.Code, qt.Equals, errors.ErrCensusParticipantNotFound.Code)

		// a complete member is unaffected
		postProcessBundleAuth0(t, bundleID, &handlers.AuthRequest{Name: "Jane", NationalID: "DNI002"})
	})

	t.Run("a member without the 2FA channel cannot log in", func(t *testing.T) {
		c := qt.New(t)
		censusID := publishCensus(t, db.OrgMemberAuthFields{}, db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail})
		bundleID, _ := postProcessBundle(t, adminToken, censusID, randomProcessID())

		err := postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{Email: "   "})
		c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		err = postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{Email: "twin@example.com"})
		c.Assert(err.Code, qt.Equals, errors.ErrCensusParticipantNotFound.Code)
	})
}
