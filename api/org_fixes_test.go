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

// TestAPIKeyConfinedToItsOrganization covers the regression where an API key inherited every
// role of its creating user: a key minted for one organization could act on any unrelated
// organization the creator belonged to. A key must only reach its own organization and the
// organizations it manages.
func TestAPIKeyConfinedToItsOrganization(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "apikeyscopepass123")
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)

	// the key's organization (integrator) and an unrelated organization the same user admins
	orgAddr := testCreateOrganization(t, token)
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	otherToken := testCreateUser(t, "apikeyotherpass123")
	otherOrgAddr := testCreateOrganization(t, otherToken)
	c.Assert(testDB.AddUserToOrganization(me.Email, otherOrgAddr, db.AdminRole), qt.IsNil)

	// mint a members:write + managed:write key for the integrator org
	created := requestAndParse[apicommon.CreateAPIKeyResponse](t, http.MethodPost, token,
		&apicommon.CreateAPIKeyRequest{Label: "scoped", Scopes: []string{ScopeMembersWrite, ScopeManagedWrite}},
		"integrator", "organizations", orgAddr.String(), "apikeys")
	apiKey := created.Secret

	// the JWT session still reaches both organizations
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, token, nil,
		"organizations", otherOrgAddr.String(), "members")

	// the key reaches its own organization...
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, apiKey, nil,
		"organizations", orgAddr.String(), "members")

	// ...and an organization it manages...
	managed := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, apiKey,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType)},
		}, "integrator", "organizations")
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, apiKey, nil,
		"organizations", managed.Address.String(), "members")

	// ...but not the unrelated organization, even though its creating user is an admin there
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, apiKey, nil,
		"organizations", otherOrgAddr.String(), "members")
}

// TestUpsertMemberEnforcesMemberQuota covers the regression where the single-member PUT skipped
// the member-base quota the bulk POST enforces: creations past the plan limit must be refused,
// while edits of existing members at the limit stay allowed.
func TestUpsertMemberEnforcesMemberQuota(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "memberquotapass123")
	orgAddr := testCreateOrganization(t, token)

	members := postOrgMembers(t, token, orgAddr, newOrgMembers(2)...)
	c.Assert(members, qt.HasLen, 2)

	// shrink the default plan's member limit to the current member base
	reducedFreePlan := *mockFreePlan
	reducedFreePlan.Organization.MaxCensus = 2
	c.Assert(testDB.SetPlan(&reducedFreePlan), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.SetPlan(mockFreePlan), qt.IsNil) })

	// creating one more member via PUT must be refused with the quota error
	newMember := newOrgMembers(3)[2]
	apiErr := putOrgMemberAndExpectError(t, token, orgAddr, newMember)
	c.Assert(apiErr.Code, qt.Equals, errors.ErrExceedsOrganizationMembersLimit.Code)

	// editing an existing member at the limit is still allowed
	edited := members[0]
	edited.Phone = "" // the API returns a trimmed hash, not a phone number
	edited.Name = "Edited At Limit"
	putOrgMember(t, token, orgAddr, edited)
}

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
	c.Assert(testDB.CreateInvitation(&db.OrganizationInvite{
		ID:                  bson.NewObjectID(),
		InvitationCode:      "fix33code",
		OrganizationAddress: orgAddr,
		CurrentUserID:       admin.ID,
		NewUserEmail:        user.Email,
		Role:                db.ManagerRole,
		Expiration:          time.Now().Add(time.Hour),
	}), qt.IsNil)
	requestAndAssertCode(http.StatusConflict, t, http.MethodPost, userToken,
		&apicommon.AcceptOrganizationInvitation{Code: "fix33code"},
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

// TestGroupUpdateRejectsOverlappingLists covers the regression where a member present in both
// addMembers and removeMembers was revoked from the group censuses yet kept in the group.
func TestGroupUpdateRejectsOverlappingLists(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "groupoverlapass123")
	orgAddr := testCreateOrganization(t, token)

	members := postOrgMembers(t, token, orgAddr, newOrgMembers(2)...)
	c.Assert(members, qt.HasLen, 2)

	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](t, http.MethodPost, token,
		&apicommon.CreateOrganizationMemberGroupRequest{
			Title:     "Overlap Group",
			MemberIDs: []string{members[0].ID},
		}, "organizations", orgAddr.String(), "groups")

	// a member in both lists is refused before anything is touched
	requestAndAssertError(errors.ErrInvalidData, t, http.MethodPut, token,
		&apicommon.UpdateOrganizationMemberGroupsRequest{
			AddMembers:    []string{members[1].ID},
			RemoveMembers: []string{members[1].ID},
		}, "organizations", orgAddr.String(), "groups", group.ID)

	// the group is unchanged
	updated, err := testDB.OrganizationMemberGroup(group.ID, orgAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(updated.MemberIDs, qt.DeepEquals, []string{members[0].ID})
}
