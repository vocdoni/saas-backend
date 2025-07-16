package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
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
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Get the organization to verify it exists
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Create a second user to be invited to the organization
	userToken := testCreateUser(t, "userpassword123")

	// Get the second user's info
	resp, code = testRequest(t, http.MethodGet, userToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var userInfo apicommon.UserInfo
	err := parseJSON(resp, &userInfo)
	c.Assert(err, qt.IsNil)
	t.Logf("User ID: %d\n", userInfo.ID)

	// Invite the second user to the organization as a viewer
	inviteRequest := &apicommon.OrganizationInvite{
		Email: userInfo.Email,
		Role:  string(db.ViewerRole),
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, userInfo.Email)
	c.Assert(err, qt.IsNil)

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
	err = parseJSON(resp, &users)
	c.Assert(err, qt.IsNil)
	c.Assert(len(users.Users), qt.Equals, 2) // Admin + new user

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
		t.Logf("Created organization with address: %s\n", newOrgAddress.String())

		// Subscribe the organization to a plan
		plans, err := testDB.Plans()
		c.Assert(err, qt.IsNil)
		c.Assert(len(plans), qt.Not(qt.Equals), 0)
		orgPlan := plans[1]

		err = testDB.SetOrganizationSubscription(newOrgAddress, &db.OrganizationSubscription{
			PlanID:          orgPlan.ID,
			StartDate:       time.Now(),
			RenewalDate:     time.Now().Add(time.Hour * 24),
			LastPaymentDate: time.Now(),
			Active:          true,
			MaxCensusSize:   1000,
		})
		c.Assert(err, qt.IsNil)
		// Get the initial organization to check the users counter
		resp, code := testRequest(t, http.MethodGet, adminToken, nil, "organizations", newOrgAddress.String())
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var initialOrg apicommon.OrganizationInfo
		err = parseJSON(resp, &initialOrg)
		c.Assert(err, qt.IsNil)

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
						Role:  string(db.ViewerRole),
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
		err = parseJSON(resp, &finalOrg)
		c.Assert(err, qt.IsNil)

		// After our fix, we expect the counter to be correctly incremented by nInvites,
		// but not exceed max allowed users of the subscribed plan
		expectedCount := min(initialUserCount+nInvites, orgPlan.Organization.Users)
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
		err = parseJSON(resp, &pendingInvites)
		c.Assert(err, qt.IsNil)
		expectedInvitesCount := min(nInvites, orgPlan.Organization.Users)
		c.Assert(len(pendingInvites.Invites), qt.Equals, expectedInvitesCount,
			qt.Commentf("expected %d pending invitations, got %d", expectedInvitesCount, len(pendingInvites.Invites)))
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
		err = parseJSON(resp, &updatedUsers)
		c.Assert(err, qt.IsNil)

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

		err = parseJSON(resp, &updatedUsers)
		c.Assert(err, qt.IsNil)

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

	t.Run("RemoveOrganizationUser", func(t *testing.T) {
		// Create a new organization and user for this test
		newOrgAddress := testCreateOrganization(t, adminToken)
		t.Logf("Created organization with address: %s\n", newOrgAddress.String())

		// Create a user to be removed
		userToRemoveToken := testCreateUser(t, "removepassword123")
		resp, code = testRequest(t, http.MethodGet, userToRemoveToken, nil, usersMeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var userToRemoveInfo apicommon.UserInfo
		err := parseJSON(resp, &userToRemoveInfo)
		c.Assert(err, qt.IsNil)
		t.Logf("User to remove ID: %d\n", userToRemoveInfo.ID)

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToRemoveInfo.Email,
			Role:  string(db.ViewerRole),
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		mailBody, err := testMailService.FindEmail(ctx, userToRemoveInfo.Email)
		c.Assert(err, qt.IsNil)

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
		err = parseJSON(resp, &users)
		c.Assert(err, qt.IsNil)
		c.Assert(len(users.Users), qt.Equals, 2) // Admin + new user

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
		err = parseJSON(resp, &updatedUsers)
		c.Assert(err, qt.IsNil)
		c.Assert(len(updatedUsers.Users), qt.Equals, 1) // Only admin remains

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

		// Test 4: Try to remove a non-existent user
		// Note: The current implementation returns 200 OK even for non-existent users
		// because the MongoDB UpdateOne operation doesn't return an error if no documents match
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
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

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
		err = parseJSON(resp, &adminInfo)
		c.Assert(err, qt.IsNil)

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
		t.Logf("Created organization with address: %s\n", newOrgAddress.String())

		// Create a user to be invited
		userToInviteEmail := fmt.Sprintf("invite-%s@example.com", uuid.New().String())

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  string(db.ViewerRole),
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
		err := parseJSON(resp, &pendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(pendingInvites.Invites), qt.Equals, 1)
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
		err = parseJSON(resp, &updatedPendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(updatedPendingInvites.Invites), qt.Equals, 1)
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
		t.Logf("Created another organization with address: %s\n", anotherOrgAddress.String())

		// Invite the user to the second organization
		inviteRequest = &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  string(db.ViewerRole),
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
		err = parseJSON(resp, &anotherPendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(anotherPendingInvites.Invites), qt.Equals, 1)

		anotherInvitationID := anotherPendingInvites.Invites[0].ID
		c.Assert(anotherInvitationID, qt.Not(qt.Equals), "")

		// Try to update the invitation from the wrong organization
		_, code = testRequest(
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
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

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
		t.Logf("Created organization with address: %s\n", newOrgAddress.String())

		// Create a user to be invited
		userToInviteEmail := fmt.Sprintf("invite-%s@example.com", uuid.New().String())

		// Invite the user to the organization
		inviteRequest := &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  string(db.ViewerRole),
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
		err := parseJSON(resp, &pendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(pendingInvites.Invites), qt.Equals, 1)
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

		err = parseJSON(resp, &pendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(pendingInvites.Invites), qt.Equals, 0)

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
		t.Logf("Created another organization with address: %s\n", anotherOrgAddress.String())

		// Invite the user to the second organization
		inviteRequest = &apicommon.OrganizationInvite{
			Email: userToInviteEmail,
			Role:  string(db.ViewerRole),
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
		err = parseJSON(resp, &anotherPendingInvites)
		c.Assert(err, qt.IsNil)
		c.Assert(len(anotherPendingInvites.Invites), qt.Equals, 1)

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

	t.Run("MaxUsersReached", func(t *testing.T) {
		c := qt.New(t)

		// Create an admin user
		adminToken := testCreateUser(t, "adminpassword123")

		// Create an organization
		orgAddress := testCreateOrganization(t, adminToken)
		t.Logf("Created organization with address: %s\n", orgAddress)

		// Get the organization from the database
		org, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)

		// Set the organization's subscription plan to plan ID 1 (which has a user limit of 10)
		// and set the user counter to the maximum allowed by the plan
		org.Subscription.PlanID = 1
		org.Counters.Users = 10 // Max users allowed by plan ID 1
		err = testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		// Try to invite a user to the organization (should fail with "max users reached")
		inviteRequest := &apicommon.OrganizationInvite{
			Email: "maxusers@example.com",
			Role:  string(db.ViewerRole),
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
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK, qt.Commentf("expected error, got success: %s", resp))

		// Verify the error message contains "max users reached"
		var errorResp struct {
			Error string `json:"error"`
			Code  int    `json:"code"`
		}
		err = json.Unmarshal(resp, &errorResp)
		c.Assert(err, qt.IsNil)
		c.Assert(errorResp.Error, qt.Contains, "max users reached")
	})
}
