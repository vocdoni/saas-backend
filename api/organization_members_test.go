package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
)

func TestOrganizationMembers(t *testing.T) {
	c := qt.New(t)

	// Create an admin user
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	resp, code := testRequest(t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("Admin user: %s\n", resp)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)
	t.Logf("Created organization with address: %s\n", orgAddress.String())

	// Get the organization to verify it exists
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Create a second user to be invited to the organization
	memberToken := testCreateUser(t, "memberpassword123")

	// Get the second user's info
	resp, code = testRequest(t, http.MethodGet, memberToken, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)

	var memberInfo apicommon.UserInfo
	err := parseJSON(resp, &memberInfo)
	c.Assert(err, qt.IsNil)
	t.Logf("Member user ID: %d\n", memberInfo.ID)

	// Invite the second user to the organization as a viewer
	inviteRequest := &apicommon.OrganizationInvite{
		Email: memberInfo.Email,
		Role:  string(db.ViewerRole),
	}
	resp, code = testRequest(
		t,
		http.MethodPost,
		adminToken,
		inviteRequest,
		"organizations",
		orgAddress.String(),
		"members",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the invitation code from the email
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mailBody, err := testMailService.FindEmail(ctx, memberInfo.Email)
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
		memberToken,
		acceptRequest,
		"organizations",
		orgAddress.String(),
		"members",
		"accept",
	)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	// Get the organization members to verify the second user is now a member
	resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

	var members apicommon.OrganizationMembers
	err = parseJSON(resp, &members)
	c.Assert(err, qt.IsNil)
	c.Assert(len(members.Members), qt.Equals, 2) // Admin + new member

	// Find the member in the list
	var memberID uint64
	var initialRole string
	for _, member := range members.Members {
		if member.Info.Email == memberInfo.Email {
			memberID = member.Info.ID
			initialRole = member.Role
			break
		}
	}
	c.Assert(memberID, qt.Not(qt.Equals), uint64(0), qt.Commentf("Member not found in organization"))
	c.Assert(initialRole, qt.Equals, string(db.ViewerRole))

	t.Run("UpdateOrganizationMemberRole", func(t *testing.T) {
		// Test 1: Update the member's role from Viewer to Manager
		updateRequest := &apicommon.UpdateOrganizationMemberRoleRequest{
			Role: string(db.ManagerRole),
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the role was updated
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var updatedMembers apicommon.OrganizationMembers
		err = parseJSON(resp, &updatedMembers)
		c.Assert(err, qt.IsNil)

		var updatedRole string
		for _, member := range updatedMembers.Members {
			if member.Info.ID == memberID {
				updatedRole = member.Role
				break
			}
		}
		c.Assert(updatedRole, qt.Equals, string(db.ManagerRole))

		// Test 2: Update the member's role from Manager to Admin
		updateRequest = &apicommon.UpdateOrganizationMemberRoleRequest{
			Role: string(db.AdminRole),
		}
		resp, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
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
			"members",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		err = parseJSON(resp, &updatedMembers)
		c.Assert(err, qt.IsNil)

		for _, member := range updatedMembers.Members {
			if member.Info.ID == memberID {
				updatedRole = member.Role
				break
			}
		}
		c.Assert(updatedRole, qt.Equals, string(db.AdminRole))

		// Test 3: Try to update with an invalid role
		updateRequest = &apicommon.UpdateOrganizationMemberRoleRequest{
			Role: "invalid_role",
		}
		_, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Test 4: Try to update without authentication
		updateRequest = &apicommon.UpdateOrganizationMemberRoleRequest{
			Role: string(db.ViewerRole),
		}
		_, code = testRequest(
			t,
			http.MethodPut,
			"",
			updateRequest,
			"organizations",
			orgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
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
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 6: Try to update a non-existent member
		// Note: The current implementation returns 200 OK even for non-existent members
		// because the MongoDB UpdateOne operation doesn't return an error if no documents match
		_, code = testRequest(
			t,
			http.MethodPut,
			adminToken,
			updateRequest,
			"organizations",
			orgAddress.String(),
			"members",
			"999999",
		)
		c.Assert(code, qt.Equals, http.StatusOK)
	})

	t.Run("RemoveOrganizationMember", func(t *testing.T) {
		// Create a new organization and user for this test
		newOrgAddress := testCreateOrganization(t, adminToken)
		t.Logf("Created organization with address: %s\n", newOrgAddress.String())

		// Create a user to be removed
		userToRemoveToken := testCreateUser(t, "removepassword123")
		resp, code = testRequest(t, http.MethodGet, userToRemoveToken, nil, usersMeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK)

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
			"members",
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
			"members",
			"accept",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Get the organization members to verify the user is now a member
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"members",
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var members apicommon.OrganizationMembers
		err = parseJSON(resp, &members)
		c.Assert(err, qt.IsNil)
		c.Assert(len(members.Members), qt.Equals, 2) // Admin + new member

		// Find the member in the list
		var memberID uint64
		for _, member := range members.Members {
			if member.Info.Email == userToRemoveInfo.Email {
				memberID = member.Info.ID
				break
			}
		}
		c.Assert(memberID, qt.Not(qt.Equals), uint64(0), qt.Commentf("Member not found in organization"))

		// Test 1: Remove the member from the organization
		resp, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		// Verify the member was removed
		resp, code = testRequest(t, http.MethodGet, adminToken, nil, "organizations", newOrgAddress.String(), "members")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))

		var updatedMembers apicommon.OrganizationMembers
		err = parseJSON(resp, &updatedMembers)
		c.Assert(err, qt.IsNil)
		c.Assert(len(updatedMembers.Members), qt.Equals, 1) // Only admin remains

		// Test 2: Try to remove a member without authentication
		_, code = testRequest(
			t,
			http.MethodDelete,
			"",
			nil,
			"organizations",
			newOrgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 3: Try to remove a member with a non-admin user
		// Create a third user who is not an admin of the organization
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		_, code = testRequest(
			t,
			http.MethodDelete,
			nonAdminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"members",
			fmt.Sprintf("%d", memberID),
		)
		c.Assert(code, qt.Equals, http.StatusUnauthorized)

		// Test 4: Try to remove a non-existent member
		// Note: The current implementation returns 200 OK even for non-existent members
		// because the MongoDB UpdateOne operation doesn't return an error if no documents match
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			newOrgAddress.String(),
			"members",
			"999999",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

		// Test 5: Try to remove yourself (which should fail)
		// Get the admin's user ID
		resp, code = testRequest(
			t,
			http.MethodGet,
			adminToken,
			nil,
			usersMeEndpoint,
		)
		c.Assert(code, qt.Equals, http.StatusOK)

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
			"members",
			fmt.Sprintf("%d", adminInfo.ID),
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)
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
			"members",
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
			"members",
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
			"members",
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
			"members",
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
			"members",
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
			"members",
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
			"members",
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
			"members",
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
			"members",
			"pending",
		)
		c.Assert(code, qt.Equals, http.StatusOK)

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
			"members",
			"pending",
			anotherInvitationID,
		)
		c.Assert(code, qt.Not(qt.Equals), http.StatusOK)

		// Clean up: Delete the invitation from the correct organization
		_, code = testRequest(
			t,
			http.MethodDelete,
			adminToken,
			nil,
			"organizations",
			anotherOrgAddress.String(),
			"members",
			"pending",
			anotherInvitationID,
		)
		c.Assert(code, qt.Equals, http.StatusOK)
	})
}
