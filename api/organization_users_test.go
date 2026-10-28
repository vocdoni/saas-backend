package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestOrganizationUsers(t *testing.T) {
	c := qt.New(t)

	// Create an admin user
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	resp, code := testRequest(t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("Admin user: %s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Create a second user to be invited to the organization
	userToken := testCreateUser(t, "userpassword123")

	// Get the second user's info
	resp, code = testRequest(t, http.MethodGet, userToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var userInfo apicommon.UserInfo
	c.Assert(json.Unmarshal(resp, &userInfo), qt.IsNil)
	t.Logf("User ID: %d\n", userInfo.ID)

	// Invite the second user to the organization as a viewer
	inviteRequest := &apicommon.OrganizationInvite{
		Email: userInfo.Email,
		Role:  db.ViewerRole,
	}
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		inviteRequest,
		"organizations",
		orgAddress.String(),
		"users",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the invitation code from the email
	mailBody := waitForEmail(t, userInfo.Email)

	// Extract the verification code using regex
	mailCode := apiTestVerificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(mailCode) > 1, qt.IsTrue)
	invitationCode := mailCode[1]
	t.Logf("Invitation code: %s\n", invitationCode)

	// Accept the invitation
	acceptRequest := &apicommon.AcceptOrganizationInvitation{
		Code: invitationCode,
	}
	resp, code = testRequest(
		t,
		http.MethodPost,
		userToken,
		acceptRequest,
		"organizations",
		orgAddress.String(),
		"users",
		"accept",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the organization users to verify the second user is now a user
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "users")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var users apicommon.OrganizationUsers
	c.Assert(json.Unmarshal(resp, &users), qt.IsNil)
	c.Assert(users.Users, qt.HasLen, 2) // Admin + new user

	// Find the user in the list
	var userID uint64
	var initialRole string
	for _, user := range users.Users {
		if user.Info.Email == userInfo.Email {
			userID = user.Info.ID
			initialRole = user.Role
			break
		}
	}
	c.Assert(userID, qt.Not(qt.Equals), uint64(0), qt.Commentf("User not found in organization"))
	c.Assert(initialRole, qt.Equals, string(db.ViewerRole))

	// Test for race condition in inviteOrganizationUserHandler
	t.Run("RaceConditionInviteUsers", func(t *testing.T) {
		// Create a new organization for this test
		newOrgAddress := testCreateOrganization(t, adminToken)

		// Get the initial organization to check the users counter
		resp, code := testRequest(t, http.MethodGet, adminToken, nil, "organizations", newOrgAddress.String())
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var initialOrg apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &initialOrg), qt.IsNil)

		initialUserCount := initialOrg.Counters.Users
		t.Logf("Initial users counter: %d", initialUserCount)

		nInvites := 15
		t.Logf("Will invite %d users", nInvites)
		var wg sync.WaitGroup
		wg.Add(nInvites)

		// Send invites concurrently to trigger the race condition
		for range nInvites {
			go func() {
				defer wg.Done()
				resp, code := testRequest(
					t,
					http.MethodPost,
					adminToken,
					&apicommon.OrganizationInvite{
						Email: fmt.Sprintf("user-%s@example.com", uuid.New().String()),
						Role:  db.ViewerRole,
					},
					"organizations",
					newOrgAddress.String(),
					"users",
				)
				c.Logf("response (%d): %s", code, resp)
			}()
		}
		// Wait for all invites to complete
		wg.Wait()

		// Wait a bit more to ensure all DB operations complete
		time.Sleep(500 * time.Millisecond)

		// Get the organization again to check the users counter
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", newOrgAddress.String())
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var finalOrg apicommon.OrganizationInfo
		c.Assert(json.Unmarshal(resp, &finalOrg), qt.IsNil)

		// every invite must be counted exactly once: no plan caps the users counter anymore
		expectedCount := initialUserCount + nInvites
		t.Logf("Final users counter: %d (expected %d)",
			finalOrg.Counters.Users, expectedCount)

		c.Assert(finalOrg.Counters.Users, qt.Equals, expectedCount,
			qt.Commentf("race condition detected: expected users counter to be %d, got %d",
				expectedCount, finalOrg.Counters.Users))

		// Verify all invitations were actually created by checking pending invitations
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var pendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &pendingInvites), qt.IsNil)
		c.Assert(pendingInvites.Invites, qt.HasLen, nInvites)
	})

	t.Run("UpdateOrganizationUserRole", func(t *testing.T) {
		// Test 1: Update the user's role from Viewer to Manager
		updateRequest := &apicommon.UpdateOrganizationUserRoleRequest{
			Role: string(db.ManagerRole),
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the role was updated
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "users")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var updatedUsers apicommon.OrganizationUsers
		c.Assert(json.Unmarshal(resp, &updatedUsers), qt.IsNil)

		var updatedRole string
		for _, user := range updatedUsers.Users {
			if user.Info.ID == userID {
				updatedRole = user.Role
				break
			}
		}
		c.Assert(updatedRole, qt.Equals, string(db.ManagerRole))

		// Test 2: Update the user's role from Manager to Admin
		updateRequest = &apicommon.UpdateOrganizationUserRoleRequest{
			Role: string(db.AdminRole),
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the role was updated
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			orgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		c.Assert(json.Unmarshal(resp, &updatedUsers), qt.IsNil)

		for _, user := range updatedUsers.Users {
			if user.Info.ID == userID {
				updatedRole = user.Role
				break
			}
		}
		c.Assert(updatedRole, qt.Equals, string(db.AdminRole))

		// Test 3: Try to update with an invalid role
		updateRequest = &apicommon.UpdateOrganizationUserRoleRequest{
			Role: "invalid_role",
		}
		_, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 4: Try to update without authentication
		updateRequest = &apicommon.UpdateOrganizationUserRoleRequest{
			Role: string(db.ViewerRole),
		}
		_, code = testRequest(
			t,
			http.MethodPut,
			"",
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 5: Try to update with a non-admin user
		// Create a third user who is not an admin of the organization
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodPut,
			nonAdminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 6: Try to update a non-existent user
		// Note: The current implementation returns 200 OK even for non-existent users
		// because the MongoDB UpdateOne operation doesn't return an error if no documents match
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"users",
			"999999",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	})

	t.Run("LastAdminCannotBeDemoted", func(t *testing.T) {
		// Fresh org whose creator is the sole admin.
		soleAdminToken := testCreateUser(t, "soleadminpassword123")
		soleOrgAddress := testCreateOrganization(t, soleAdminToken)

		resp, code := testRequest(t, http.MethodGet, soleAdminToken, nil, usersMeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
		var soleAdminInfo apicommon.UserInfo
		c.Assert(json.Unmarshal(resp, &soleAdminInfo), qt.IsNil)

		// Demoting the only admin must be rejected, otherwise the org is orphaned.
		demote := &apicommon.UpdateOrganizationUserRoleRequest{Role: string(db.ViewerRole)}
		errResp := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest,
			t,
			http.MethodPut,
			soleAdminToken,
			demote,
			"organizations",
			soleOrgAddress.String(),
			"users",
			fmt.Sprintf("%d", soleAdminInfo.ID),
		)
		c.Assert(errResp.Code, qt.Equals, errors.ErrInvalidUserData.Code)

		// The admin role must be unchanged.
		role, ok := db.UserRole(""), false
		users, err := testDB.OrganizationUsers(soleOrgAddress)
		c.Assert(err, qt.IsNil)
		for _, u := range users {
			if u.ID == soleAdminInfo.ID {
				role, ok = u.RoleFor(soleOrgAddress)
				break
			}
		}
		c.Assert(ok, qt.IsTrue)
		c.Assert(role, qt.Equals, db.AdminRole)
	})

	t.Run("RemoveOrganizationUser", func(t *testing.T) {
		// Create a new organization and user for this test
		newOrgAddress := testCreateOrganization(t, adminToken)

		// Create a user to be removed
		userToRemoveToken := testCreateUser(t, "removepassword123")
		resp, code = testRequest(t, http.MethodGet, userToRemoveToken, nil, usersMeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var userToRemoveInfo apicommon.UserInfo
		c.Assert(json.Unmarshal(resp, &userToRemoveInfo), qt.IsNil)
		t.Logf("User to remove ID: %d\n", userToRemoveInfo.ID)

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToRemoveInfo.Email,
			Role:  db.ViewerRole,
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			newOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Get the invitation code from the email
		mailBody := waitForEmail(t, userToRemoveInfo.Email)

		// Extract the verification code using regex
		mailCode := apiTestVerificationCodeRgx.FindStringSubmatch(mailBody)
		c.Assert(len(mailCode) > 1, qt.IsTrue)
		invitationCode := mailCode[1]

		// Accept the invitation
		acceptRequest := &apicommon.AcceptOrganizationInvitation{
			Code: invitationCode,
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			userToRemoveToken,
			acceptRequest,
			"organizations",
			newOrgAddress.String(),
			"users",
			"accept",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Get the organization users to verify the user is now a user
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var users apicommon.OrganizationUsers
		c.Assert(json.Unmarshal(resp, &users), qt.IsNil)
		c.Assert(users.Users, qt.HasLen, 2) // Admin + new user

		// Find the user in the list
		var userID uint64
		for _, user := range users.Users {
			if user.Info.Email == userToRemoveInfo.Email {
				userID = user.Info.ID
				break
			}
		}
		c.Assert(userID, qt.Not(qt.Equals), uint64(0), qt.Commentf("User not found in organization"))

		// Test 1: Remove the user from the organization
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the user was removed
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", newOrgAddress.String(), "users")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var updatedUsers apicommon.OrganizationUsers
		c.Assert(json.Unmarshal(resp, &updatedUsers), qt.IsNil)
		c.Assert(updatedUsers.Users, qt.HasLen, 1) // Only admin remains

		// Test 2: Try to remove a user without authentication
		_, code = testRequest(
			t,
			http.MethodDelete,
			"",
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 3: Try to remove a user with a non-admin user
		// Create a third user who is not an admin of the organization
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodDelete,
			nonAdminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			fmt.Sprintf("%d", userID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to remove a non-existent user: nothing is removed, so it is refused
		// rather than decrementing the users counter
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"999999",
		)
		c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))

		// Test 5: Try to remove yourself (which should fail)
		// Get the admin's user ID
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			usersMeEndpoint,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var adminInfo apicommon.UserInfo
		c.Assert(json.Unmarshal(resp, &adminInfo), qt.IsNil)

		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			fmt.Sprintf("%d", adminInfo.ID),
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
	})

	t.Run("UpdatePendingInvitation", func(t *testing.T) {
		// Create a new organization for this test
		newOrgAddress := testCreateOrganization(t, adminToken)

		// Create a user to be invited
		userToInviteEmail := fmt.Sprintf("invite-%s@example.com", uuid.New().String())

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  db.ViewerRole,
		}
		resp, code := testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			newOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the invitation was created by checking pending invitations
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var pendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &pendingInvites), qt.IsNil)
		c.Assert(pendingInvites.Invites, qt.HasLen, 1)
		c.Assert(pendingInvites.Invites[0].Email, qt.Equals, userToInviteEmail)

		// Get the invitation ID and initial expiration time
		invitationID := pendingInvites.Invites[0].ID
		c.Assert(invitationID, qt.Not(qt.Equals), "")
		initialExpiration := pendingInvites.Invites[0].Expiration

		// Test 1: Update the pending invitation
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			nil, // No request body needed
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID, // Add invitationID as path parameter
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the invitation was updated by checking pending invitations again
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var updatedPendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &updatedPendingInvites), qt.IsNil)
		c.Assert(updatedPendingInvites.Invites, qt.HasLen, 1)
		c.Assert(updatedPendingInvites.Invites[0].Email, qt.Equals, userToInviteEmail)

		// Verify the expiration time has been updated (should be later than the initial one)
		c.Assert(updatedPendingInvites.Invites[0].Expiration.After(initialExpiration), qt.IsTrue)

		// Test 2: Try to update a non-existent invitation
		nonExistentID := "000000000000000000000000" // Invalid ObjectID format
		_, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			nonExistentID,
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 3: Try to update without authentication
		_, code = testRequest(
			t,
			http.MethodPut,
			"",
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to update with a non-admin user
		// Create a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodPut,
			nonAdminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 5: Create another organization and invitation, then try to update it from the wrong organization
		anotherOrgAddress := testCreateOrganization(t, adminToken)

		// Invite the user to the second organization
		inviteRequest = &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  db.ViewerRole,
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			anotherOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Get the invitation ID from the second organization
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			anotherOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var anotherPendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &anotherPendingInvites), qt.IsNil)
		c.Assert(anotherPendingInvites.Invites, qt.HasLen, 1)

		anotherInvitationID := anotherPendingInvites.Invites[0].ID
		c.Assert(anotherInvitationID, qt.Not(qt.Equals), "")

		// Try to update the invitation from the wrong organization
		errResp := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest,
			t,
			http.MethodPut,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(), // Using first org to update invitation from second org
			"users",
			"pending",
			anotherInvitationID,
		)
		c.Assert(errResp.Code, qt.Equals, errors.ErrInvalidData.Code)

		// Clean up: Delete the invitations
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			anotherOrgAddress.String(),
			"users",
			"pending",
			anotherInvitationID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	})

	t.Run("DeletePendingInvitation", func(t *testing.T) {
		// Create a new organization for this test
		newOrgAddress := testCreateOrganization(t, adminToken)

		// Create a user to be invited
		userToInviteEmail := fmt.Sprintf("invite-%s@example.com", uuid.New().String())

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  db.ViewerRole,
		}
		resp, code := testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			newOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the invitation was created by checking pending invitations
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var pendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &pendingInvites), qt.IsNil)
		c.Assert(pendingInvites.Invites, qt.HasLen, 1)
		c.Assert(pendingInvites.Invites[0].Email, qt.Equals, userToInviteEmail)

		// Get the invitation ID
		invitationID := pendingInvites.Invites[0].ID
		c.Assert(invitationID, qt.Not(qt.Equals), "")

		// Test 1: Delete the pending invitation
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil, // No request body needed
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID, // Add invitationID as path parameter
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the invitation was deleted by checking pending invitations again
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		c.Assert(json.Unmarshal(resp, &pendingInvites), qt.IsNil)
		c.Assert(pendingInvites.Invites, qt.HasLen, 0)

		// Test 2: Try to delete a non-existent invitation
		nonExistentID := "000000000000000000000000" // Invalid ObjectID format
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			nonExistentID,
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 3: Try to delete without authentication
		_, code = testRequest(
			t,
			http.MethodDelete,
			"",
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to delete with a non-admin user
		// Create a non-admin user
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodDelete,
			nonAdminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"users",
			"pending",
			invitationID,
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 5: Create another organization and invitation, then try to delete it from the wrong organization
		anotherOrgAddress := testCreateOrganization(t, adminToken)

		// Invite the user to the second organization
		inviteRequest = &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  db.ViewerRole,
		}
		resp, code = testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			anotherOrgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Get the invitation ID from the second organization
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			anotherOrgAddress.String(),
			"users",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var anotherPendingInvites apicommon.OrganizationInviteList
		c.Assert(json.Unmarshal(resp, &anotherPendingInvites), qt.IsNil)
		c.Assert(anotherPendingInvites.Invites, qt.HasLen, 1)

		anotherInvitationID := anotherPendingInvites.Invites[0].ID
		c.Assert(anotherInvitationID, qt.Not(qt.Equals), "")

		// Try to delete the invitation from the wrong organization
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(), // Using first org to delete invitation from second org
			"users",
			"pending",
			anotherInvitationID,
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Clean up: Delete the invitation from the correct organization
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			anotherOrgAddress.String(),
			"users",
			"pending",
			anotherInvitationID,
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	})

	t.Run("UsersCounterNotCappedByPlan", func(t *testing.T) {
		c := qt.New(t)

		// Create an admin user
		adminToken := testCreateUser(t, "adminpassword123")

		// Create an organization
		orgAddress := testCreateOrganization(t, adminToken)

		// Get the organization from the database
		org, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)

		// Set the user counter to the free plan's former users limit: no plan caps invites anymore.
		org.Subscription.PlanID = mockFreePlan.ID
		c.Assert(testDB.SetOrganizationSubscription(orgAddress, &org.Subscription), qt.IsNil)
		org.Counters.Users = 10
		err = testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Inviting a further user still succeeds
		inviteRequest := &apicommon.OrganizationInvite{
			Email: "maxusers@example.com",
			Role:  db.ViewerRole,
		}
		resp, code := testRequest(
			t,
			http.MethodPost,
			adminToken,
			inviteRequest,
			"organizations",
			orgAddress.String(),
			"users",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
		org, err = testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(org.Counters.Users, qt.Equals, 11)
	})

	t.Run("OrganizationRoles", func(t *testing.T) {
		// Test the GET /organizations/roles endpoint to verify the new permission structure
		resp, code := testRequest(t, http.MethodGet, "", nil, "organizations", "roles")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var roles apicommon.OrganizationRoleList
		c.Assert(json.Unmarshal(resp, &roles), qt.IsNil)

		// Verify we have the expected roles
		c.Assert(roles.Roles, qt.HasLen, 3) // Admin, Manager, Viewer

		// Create a map for easier verification
		roleMap := make(map[string]*apicommon.OrganizationRole)
		for _, role := range roles.Roles {
			roleMap[role.Role] = role
		}

		// Verify Admin role permissions
		adminRole, exists := roleMap["admin"]
		c.Assert(exists, qt.IsTrue, qt.Commentf("Admin role not found"))
		c.Assert(adminRole.Name, qt.Equals, "Admin")
		c.Assert(adminRole.OrganizationWritePermission, qt.IsTrue)
		c.Assert(adminRole.ProcessWritePermission, qt.IsTrue)

		// Verify Manager role permissions
		managerRole, exists := roleMap["manager"]
		c.Assert(exists, qt.IsTrue, qt.Commentf("Manager role not found"))
		c.Assert(managerRole.Name, qt.Equals, "Manager")
		c.Assert(managerRole.OrganizationWritePermission, qt.IsFalse)
		c.Assert(managerRole.ProcessWritePermission, qt.IsTrue)

		// Verify Viewer role permissions
		viewerRole, exists := roleMap["viewer"]
		c.Assert(exists, qt.IsTrue, qt.Commentf("Viewer role not found"))
		c.Assert(viewerRole.Name, qt.Equals, "Viewer")
		c.Assert(viewerRole.OrganizationWritePermission, qt.IsFalse)
		c.Assert(viewerRole.ProcessWritePermission, qt.IsFalse)

		t.Logf("Organization roles response: %s", resp)
	})
}

// TestOrganizationWriterCap drives the 5 admins/managers cap over HTTP: seats are taken by users and
// pending invitations alike, viewers never take one, and only a promotion to a write role is checked.
func TestOrganizationWriterCap(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	usersPath := []string{"organizations", orgAddress.String(), "users"}

	invite := func(email string, role db.UserRole) {
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, adminToken,
			&apicommon.OrganizationInvite{Email: email, Role: role}, usersPath...)
	}
	// joinAsViewer creates a user, invites it as a viewer, accepts and returns its id
	joinAsViewer := func() uint64 {
		token := testCreateUser(t, "userpassword123")
		info := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
		invite(info.Email, db.ViewerRole)
		code := apiTestVerificationCodeRgx.FindStringSubmatch(waitForEmail(t, info.Email))
		c.Assert(len(code) > 1, qt.IsTrue)
		requestAndAssertCode(http.StatusOK, t, http.MethodPost, token,
			&apicommon.AcceptOrganizationInvitation{Code: code[1]}, append(usersPath, "accept")...)
		return info.ID
	}
	setRole := func(userID uint64, role db.UserRole) func() {
		return func() {
			requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken,
				&apicommon.UpdateOrganizationUserRoleRequest{Role: string(role)},
				append(usersPath, fmt.Sprint(userID))...)
		}
	}
	randomEmail := func() string { return fmt.Sprintf("user-%s@example.com", uuid.New().String()) }

	// the creator is the first writer; a second admin and a viewer join through invitations
	secondAdmin, viewer := joinAsViewer(), joinAsViewer()
	setRole(secondAdmin, db.AdminRole)() // 2 writers
	for range 3 {
		invite(randomEmail(), db.ManagerRole) // 5 writers, pending invitations included
	}

	requestAndAssertError(errors.ErrMaxWritersReached, t, http.MethodPost, adminToken,
		&apicommon.OrganizationInvite{Email: randomEmail(), Role: db.ManagerRole}, usersPath...)
	invite(randomEmail(), db.ViewerRole) // viewers are never capped
	requestAndAssertError(errors.ErrMaxWritersReached, t, http.MethodPut, adminToken,
		&apicommon.UpdateOrganizationUserRoleRequest{Role: string(db.ManagerRole)},
		append(usersPath, fmt.Sprint(viewer))...)
	// a swap between write roles takes no new seat
	setRole(secondAdmin, db.ManagerRole)()

	// an expired write invitation frees its seat, and reviving it must take one again
	pending, err := testDB.PendingInvitations(orgAddress)
	c.Assert(err, qt.IsNil)
	var expired *db.OrganizationInvite
	for i := range pending {
		if pending[i].Role == db.ManagerRole {
			expired = &pending[i]
			break
		}
	}
	c.Assert(expired, qt.Not(qt.IsNil))
	_, err = testDB.DBClient.Database(testDBName).Collection("organizationInvites").UpdateOne(context.Background(),
		bson.M{"_id": expired.ID}, bson.M{"$set": bson.M{"expiration": time.Now().Add(-time.Minute)}})
	c.Assert(err, qt.IsNil)
	invite(randomEmail(), db.ManagerRole) // the freed seat: 5 writers again
	requestAndAssertError(errors.ErrMaxWritersReached, t, http.MethodPut, adminToken, nil,
		append(usersPath, "pending", expired.ID.Hex())...)
}

// invitedOrg is an organization owned by a fresh user; no plan is needed, seats are bounded only by the writer cap.
type invitedOrg struct {
	ownerToken string
	address    common.Address
	usersPath  []string
}

func newInvitedOrg(t *testing.T) invitedOrg {
	t.Helper()
	ownerToken := testCreateUser(t, "ownerpassword123")
	address := testCreateOrganization(t, ownerToken)
	return invitedOrg{
		ownerToken: ownerToken,
		address:    address,
		usersPath:  []string{"organizations", address.String(), "users"},
	}
}

// newAdmin adds a verified user to the organization as admin through the invitation flow
func (o invitedOrg) newAdmin(t *testing.T) (string, apicommon.UserInfo) {
	t.Helper()
	token := testCreateUser(t, "adminpassword123")
	info := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, o.ownerToken,
		&apicommon.OrganizationInvite{Email: info.Email, Role: db.AdminRole}, o.usersPath...)
	code := invitationCodeFromEmail(t, info.Email)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token,
		&apicommon.AcceptOrganizationInvitation{Code: code}, append(o.usersPath, "accept")...)
	return token, info
}

// inviteAsAdmin issues an admin invitation with the given inviter token and returns the invitee email and code
func (o invitedOrg) inviteAsAdmin(t *testing.T, inviterToken string) (email, code string) {
	t.Helper()
	email = fmt.Sprintf("invitee-%s@example.com", uuid.New().String())
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, inviterToken,
		&apicommon.OrganizationInvite{Email: email, Role: db.AdminRole}, o.usersPath...)
	return email, invitationCodeFromEmail(t, email)
}

func (o invitedOrg) usersCounter(t *testing.T) int {
	t.Helper()
	info := requestAndParse[apicommon.OrganizationInfo](t, http.MethodGet, o.ownerToken, nil,
		"organizations", o.address.String())
	return info.Counters.Users
}

func (o invitedOrg) pendingCount(t *testing.T) int {
	t.Helper()
	list := requestAndParse[apicommon.OrganizationInviteList](t, http.MethodGet, o.ownerToken, nil,
		append(o.usersPath, "pending")...)
	return len(list.Invites)
}

// acceptRefused tries to accept the invitation with the given code as a new user, expecting a refusal
func (o invitedOrg) acceptRefused(t *testing.T, code string) {
	t.Helper()
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&apicommon.AcceptOrganizationInvitation{
			Code: code,
			User: &apicommon.UserInfo{FirstName: "Invitee", LastName: "Test", Password: "inviteepassword123"},
		}, append(o.usersPath, "accept")...)
}

// TestRevokedInviterInvitations covers invitations issued by an admin who then loses the role: removing or
// demoting that admin deletes their pending invitations, and accepting one whose issuer is no longer an admin
// is refused and drops the invitation without creating the invitee.
func TestRevokedInviterInvitations(t *testing.T) {
	t.Run("RemovedInviterInvitationsRevoked", func(t *testing.T) {
		c := qt.New(t)
		o := newInvitedOrg(t)
		adminToken, adminInfo := o.newAdmin(t)
		countBefore := o.usersCounter(t)
		_, code := o.inviteAsAdmin(t, adminToken)
		c.Assert(o.pendingCount(t), qt.Equals, 1)
		c.Assert(o.usersCounter(t), qt.Equals, countBefore+1)

		requestAndAssertCode(http.StatusOK, t, http.MethodDelete, o.ownerToken, nil,
			append(o.usersPath, fmt.Sprint(adminInfo.ID))...)

		// the invitation is gone and its slot released, and the invitee cannot accept it
		c.Assert(o.pendingCount(t), qt.Equals, 0)
		c.Assert(o.usersCounter(t), qt.Equals, countBefore-1)
		o.acceptRefused(t, code)
	})

	t.Run("DemotedInviterInvitationsRevoked", func(t *testing.T) {
		c := qt.New(t)
		o := newInvitedOrg(t)
		adminToken, adminInfo := o.newAdmin(t)
		countBefore := o.usersCounter(t)
		_, code := o.inviteAsAdmin(t, adminToken)
		c.Assert(o.pendingCount(t), qt.Equals, 1)

		requestAndAssertCode(http.StatusOK, t, http.MethodPut, o.ownerToken,
			&apicommon.UpdateOrganizationUserRoleRequest{Role: string(db.ViewerRole)},
			append(o.usersPath, fmt.Sprint(adminInfo.ID))...)

		c.Assert(o.pendingCount(t), qt.Equals, 0)
		c.Assert(o.usersCounter(t), qt.Equals, countBefore)
		o.acceptRefused(t, code)
	})

	t.Run("AcceptRefusedWhenInviterLostAdmin", func(t *testing.T) {
		c := qt.New(t)
		o := newInvitedOrg(t)
		adminToken, adminInfo := o.newAdmin(t)
		countBefore := o.usersCounter(t)
		inviteeEmail, code := o.inviteAsAdmin(t, adminToken)

		// drop the membership directly in the DB, bypassing the handler cleanup, as user erasure does
		removed, err := testDB.RemoveOrganizationUser(o.address, adminInfo.ID)
		c.Assert(err, qt.IsNil)
		c.Assert(removed, qt.IsTrue)

		o.acceptRefused(t, code)

		// the invitation is dropped and its slot released, and no user is created for the invitee
		c.Assert(o.pendingCount(t), qt.Equals, 0)
		c.Assert(o.usersCounter(t), qt.Equals, countBefore)
		_, err = testDB.UserByEmail(inviteeEmail)
		c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	})

	t.Run("ConcurrentRefusedAcceptsReleaseSlotOnce", func(t *testing.T) {
		c := qt.New(t)
		o := newInvitedOrg(t)
		adminToken, adminInfo := o.newAdmin(t)
		countBefore := o.usersCounter(t)
		_, code := o.inviteAsAdmin(t, adminToken)
		removed, err := testDB.RemoveOrganizationUser(o.address, adminInfo.ID)
		c.Assert(err, qt.IsNil)
		c.Assert(removed, qt.IsTrue)

		// several accepts of the same revoked code race each other; only one may release the slot
		const nAccepts = 10
		acceptPath := append(append([]string{}, o.usersPath...), "accept")
		body := &apicommon.AcceptOrganizationInvitation{
			Code: code,
			User: &apicommon.UserInfo{FirstName: "Invitee", LastName: "Test", Password: "inviteepassword123"},
		}
		statuses := make(chan int, nAccepts)
		var wg sync.WaitGroup
		for range nAccepts {
			wg.Go(func() {
				_, status := testRequest(t, http.MethodPost, "", body, acceptPath...)
				statuses <- status
			})
		}
		wg.Wait()
		close(statuses)
		for status := range statuses {
			c.Assert(status, qt.Equals, http.StatusUnauthorized)
		}

		c.Assert(o.pendingCount(t), qt.Equals, 0)
		c.Assert(o.usersCounter(t), qt.Equals, countBefore)
	})
}

// invitationCodeFromEmail extracts the invitation code from the latest email sent to the given address.
func invitationCodeFromEmail(t *testing.T, email string) string {
	t.Helper()
	mailCode := apiTestVerificationCodeRgx.FindStringSubmatch(waitForEmail(t, email))
	qt.Assert(t, len(mailCode) > 1, qt.IsTrue)
	return mailCode[1]
}
