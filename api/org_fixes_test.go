package api

import (
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestAcceptInvitationWithExistingDifferentRole covers the regression where accepting an
// invitation only checked for the exact invited role: a user already holding a different role
// got a second membership entry for the same organization.
func TestAcceptInvitationWithExistingDifferentRole(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "inviteadminpass123")
	admin := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	orgAddr := testCreateOrganization(t, adminToken)

	userToken := testCreateUser(t, "inviteduserpass123")
	user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, userToken, nil, usersMeEndpoint)

	// the user already holds a role in the organization...
	c.Assert(testDB.AddUserToOrganization(user.Email, orgAddr, db.ViewerRole), qt.IsNil)

	// ...and an invitation for a different role is accepted
	const conflictCode = "aabbccddeeff00112233445566778899" // 32 hex chars = 16 bytes
	c.Assert(testDB.CreateInvitation(&db.OrganizationInvite{
		ID:                  bson.NewObjectID(),
		InvitationCode:      conflictCode,
		OrganizationAddress: orgAddr,
		CurrentUserID:       admin.ID,
		NewUserEmail:        user.Email,
		Role:                db.ManagerRole,
		Expiration:          time.Now().Add(time.Hour),
	}), qt.IsNil)
	requestAndAssertCode(http.StatusConflict, t, http.MethodPost, userToken,
		&apicommon.AcceptOrganizationInvitation{Code: conflictCode},
		"organizations", orgAddr.String(), "users", "accept")

	// the user keeps a single membership entry with the original role
	dbUser, err := testDB.UserByEmail(user.Email)
	c.Assert(err, qt.IsNil)
	entries := 0
	for _, o := range dbUser.Organizations {
		if o.Address == orgAddr {
			entries++
			c.Assert(o.Role, qt.Equals, db.ViewerRole)
		}
	}
	c.Assert(entries, qt.Equals, 1)
}

// TestAcceptInvitationRejectsInvalidCode covers the regression where the accept handler
// would query the database for any code, including empty strings and short legacy codes.
// A code that is not exactly 2*InvitationCodeLength valid hex characters must be rejected
// before the DB lookup, regardless of what is stored.
func TestAcceptInvitationRejectsInvalidCode(t *testing.T) {
	token := testCreateUser(t, "inviteformatpass123")
	orgAddr := testCreateOrganization(t, token)

	for _, badCode := range []string{
		"",                                  // empty — matches any "" stored by no-mail deployments
		"aabb",                              // too short valid hex
		"aabbccddeeff0011223344556677889",   // one char short (31)
		"aabbccddeeff001122334455667788990", // one char too long (33)
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",  // right length but not hex
	} {
		requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
			&apicommon.AcceptOrganizationInvitation{Code: badCode},
			"organizations", orgAddr.String(), "users", "accept")
	}
}

