package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
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

// TestCreateSubOrganizationWithinPlan covers the regression where creating a suborganization
// always failed: the parent check took the length of a fixed-size address, never zero. Within the
// plan's limit a suborganization is created; past it the dedicated 400 is returned.
func TestCreateSubOrganizationWithinPlan(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "suborgplanpass123")
	parentAddr := testCreateOrganization(t, token)

	// the essential plan allows one suborganization
	parent, err := testDB.Organization(parentAddr)
	c.Assert(err, qt.IsNil)
	parent.Subscription.PlanID = mockEssentialPlan.ID
	c.Assert(testDB.SetOrganizationSubscription(parentAddr, &parent.Subscription), qt.IsNil)

	subOrgInfo := &apicommon.OrganizationInfo{
		Type:   string(db.CompanyType),
		Parent: &apicommon.OrganizationInfo{Address: parentAddr},
	}
	sub := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, subOrgInfo, organizationsEndpoint)
	c.Assert(sub.Parent, qt.Not(qt.IsNil))
	c.Assert(sub.Parent.Address, qt.Equals, parentAddr)

	// the plan is now exhausted
	requestAndAssertError(errors.ErrMaxSubOrgsReached, t, http.MethodPost, token, subOrgInfo, organizationsEndpoint)

	// a suborganization cannot be the parent of another one
	nested := &apicommon.OrganizationInfo{
		Type:   string(db.CompanyType),
		Parent: &apicommon.OrganizationInfo{Address: sub.Address},
	}
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token, nested, organizationsEndpoint)
}

// TestOrganizationBillingEmailAdminOnly covers the regression where the public organization
// endpoint returned the subscription billing email to anyone. Only admins of each organization in
// the parent chain see its billing email, on the public endpoint and on /users/me.
func TestOrganizationBillingEmailAdminOnly(t *testing.T) {
	c := qt.New(t)
	parentToken := testCreateUser(t, "billingparentpass123")
	parentAddr := testCreateOrganization(t, parentToken)
	childToken := testCreateUser(t, "billingchildpass123")
	childAddr := testCreateOrganization(t, childToken)
	viewerToken := testCreateUser(t, "billingviewerpass123")
	viewer := requestAndParse[apicommon.UserInfo](t, http.MethodGet, viewerToken, nil, usersMeEndpoint)
	c.Assert(testDB.AddUserToOrganization(viewer.Email, childAddr, db.ViewerRole), qt.IsNil)

	// give both organizations a billing email and link the child to the parent
	for addr, email := range map[common.Address]string{parentAddr: "parent@billing.test", childAddr: "child@billing.test"} {
		org, err := testDB.Organization(addr)
		c.Assert(err, qt.IsNil)
		org.Subscription.Email = email
		c.Assert(testDB.SetOrganizationSubscription(addr, &org.Subscription), qt.IsNil)
		if addr == childAddr {
			org.Parent = parentAddr
			c.Assert(testDB.SetOrganization(org), qt.IsNil)
		}
	}

	getInfo := func(token string) *apicommon.OrganizationInfo {
		info := requestAndParse[apicommon.OrganizationInfo](t, http.MethodGet, token, nil,
			"organizations", childAddr.String())
		c.Assert(info.Subscription, qt.Not(qt.IsNil))
		c.Assert(info.Parent, qt.Not(qt.IsNil))
		c.Assert(info.Parent.Subscription, qt.Not(qt.IsNil))
		return &info
	}

	// anonymous and non-admin members get no billing email at all
	for _, token := range []string{"", viewerToken} {
		info := getInfo(token)
		c.Assert(info.Subscription.Email, qt.Equals, "")
		c.Assert(info.Parent.Subscription.Email, qt.Equals, "")
	}
	// the child's admin gets the child's email, but not the parent's it does not administer
	info := getInfo(childToken)
	c.Assert(info.Subscription.Email, qt.Equals, "child@billing.test")
	c.Assert(info.Parent.Subscription.Email, qt.Equals, "")

	// /users/me applies the same rule per organization
	viewerMe := requestAndParse[apicommon.UserInfo](t, http.MethodGet, viewerToken, nil, usersMeEndpoint)
	c.Assert(viewerMe.Organizations, qt.HasLen, 1)
	c.Assert(viewerMe.Organizations[0].Organization.Subscription.Email, qt.Equals, "")
	childMe := requestAndParse[apicommon.UserInfo](t, http.MethodGet, childToken, nil, usersMeEndpoint)
	c.Assert(childMe.Organizations, qt.HasLen, 1)
	c.Assert(childMe.Organizations[0].Organization.Subscription.Email, qt.Equals, "child@billing.test")
	c.Assert(childMe.Organizations[0].Organization.Parent.Subscription.Email, qt.Equals, "")
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

// TestManagedOrgBillingEmailRedacted covers the regression where the integrator create and
// list endpoints returned subscription.email without redaction. The billing email is
// admin-only data: a non-admin integrator must not see the billing emails of managed orgs
// it does not administer.
func TestManagedOrgBillingEmailRedacted(t *testing.T) {
	c := qt.New(t)
	intToken := testCreateUser(t, "intbillingpass123")
	orgAddr := testCreateOrganization(t, intToken)

	// enable the org as an integrator with capacity for one managed org
	org, err := testDB.Organization(orgAddr)
	c.Assert(err, qt.IsNil)
	org.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// create a second user to own the managed org so the integrator is not its admin
	ownerToken := testCreateUser(t, "intbillingownerpass123")
	ownerInfo := requestAndParse[apicommon.UserInfo](t, http.MethodGet, ownerToken, nil, usersMeEndpoint)

	// create the managed org assigned to the separate owner
	created := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, intToken,
		&apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{Type: string(db.CompanyType)},
			OwnerEmail:       ownerInfo.Email,
		}, "integrator", "organizations")

	// set a billing email on the managed org
	managed, err := testDB.Organization(created.Address)
	c.Assert(err, qt.IsNil)
	managed.Subscription.Email = "managed@billing.test"
	c.Assert(testDB.SetOrganizationSubscription(created.Address, &managed.Subscription), qt.IsNil)

	// list managed orgs as the integrator (not admin of the managed org) — billing email
	// must be absent from every entry
	list := requestAndParse[apicommon.ListManagedOrganizations](t, http.MethodGet, intToken,
		nil, "integrator", "organizations")
	c.Assert(list.Organizations, qt.Not(qt.HasLen), 0)
	for _, o := range list.Organizations {
		if o.Subscription != nil {
			c.Assert(o.Subscription.Email, qt.Equals, "")
		}
	}

	// the managed org's own admin (owner) CAN see the billing email on the public org endpoint
	ownerView := requestAndParse[apicommon.OrganizationInfo](t, http.MethodGet, ownerToken, nil,
		"organizations", created.Address.String())
	c.Assert(ownerView.Subscription, qt.Not(qt.IsNil))
	c.Assert(ownerView.Subscription.Email, qt.Equals, "managed@billing.test")
}
