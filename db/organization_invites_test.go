package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	currentUserID = uint64(1)
)

func TestOrganizationInvites(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	expires := time.Now().Add(time.Hour)

	t.Run("UpdateInvitation", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// Test updating invitation with empty ID
		emptyIDInvite := &OrganizationInvite{
			Role:       ManagerRole,
			Expiration: time.Now().Add(time.Hour),
		}
		err := testDB.UpdateInvitation(emptyIDInvite)
		c.Assert(err, qt.ErrorMatches, "invitation ID cannot be empty")

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		invite := &OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		}
		err = testDB.CreateInvitation(invite)
		c.Assert(err, qt.IsNil)

		// Get invitation to retrieve its ID
		invitation, err := testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)
		c.Assert(invitation.Role, qt.Equals, AdminRole)

		// Update invitation with new role and expiration
		newExpires := time.Now().Add(time.Hour * 2)
		updatedInvite := &OrganizationInvite{
			ID:         invitation.ID,
			Role:       ManagerRole,
			Expiration: newExpires,
		}
		err = testDB.UpdateInvitation(updatedInvite)
		c.Assert(err, qt.IsNil)

		// Verify invitation was updated
		updatedInvitation, err := testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedInvitation.ID, qt.Equals, invitation.ID)
		c.Assert(updatedInvitation.InvitationCode, qt.Equals, invitationCode)
		c.Assert(updatedInvitation.OrganizationAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(updatedInvitation.CurrentUserID, qt.Equals, currentUserID)
		c.Assert(updatedInvitation.NewUserEmail, qt.Equals, newUserEmail)
		c.Assert(updatedInvitation.Role, qt.Equals, ManagerRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(updatedInvitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, newExpires.Truncate(time.Second).UTC())

		// Test updating invitation with non-existent ID
		nonExistentID, err := primitive.ObjectIDFromHex("5f50cf9b8b5cf3b5e1c8a1a1")
		c.Assert(err, qt.IsNil)
		nonExistentInvite := &OrganizationInvite{
			ID:         nonExistentID,
			Role:       ViewerRole,
			Expiration: time.Now().Add(time.Hour * 3),
		}
		err = testDB.UpdateInvitation(nonExistentInvite)
		// This should not return an error, as MongoDB's UpdateOne doesn't error when no documents match
		c.Assert(err, qt.IsNil)
	})

	t.Run("GetInvitation", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Test getting non-existent invitation
		_, err := testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Get invitation
		invitation, err := testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)
		c.Assert(invitation.InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitation.OrganizationAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(invitation.CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitation.NewUserEmail, qt.Equals, newUserEmail)
		c.Assert(invitation.Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())
	})

	t.Run("PendingInvitations", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// List invitations expecting none
		invitations, err := testDB.PendingInvitations(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(invitations, qt.HasLen, 0)

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// List invitations expecting one
		invitations, err = testDB.PendingInvitations(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(invitations, qt.HasLen, 1)
		c.Assert(invitations[0].InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitations[0].OrganizationAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(invitations[0].CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitations[0].NewUserEmail, qt.Equals, newUserEmail)
		c.Assert(invitations[0].Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitations[0].Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())

		// Delete the invitation
		err = testDB.DeleteInvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)

		// List invitations expecting none
		invitations, err = testDB.PendingInvitations(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(invitations, qt.HasLen, 0)
	})

	t.Run("DeleteInvitation", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Non existing invitation does not return an error on delete attempt
		err := testDB.DeleteInvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Verify invitation exists
		_, err = testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)

		// Delete the invitation
		err = testDB.DeleteInvitationByCode(invitationCode)
		c.Assert(err, qt.IsNil)

		// Verify invitation is deleted
		_, err = testDB.InvitationByCode(invitationCode)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})

	t.Run("InvitationByEmail", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Test getting non-existent invitation by email
		_, err := testDB.InvitationByEmail(newUserEmail)
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Get invitation by email
		invitation, err := testDB.InvitationByEmail(newUserEmail)
		c.Assert(err, qt.IsNil)
		c.Assert(invitation.InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitation.OrganizationAddress, qt.DeepEquals, testOrgAddress)
		c.Assert(invitation.CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitation.NewUserEmail, qt.Equals, newUserEmail)
		c.Assert(invitation.Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())
	})

	t.Run("DeleteInvitationByEmail", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		// Non existing invitation does not return an error on delete attempt
		err := testDB.DeleteInvitationByEmail(newUserEmail)
		c.Assert(err, qt.IsNil)

		// Create organization and user
		err = testDB.SetOrganization(&Organization{
			Address: testOrgAddress,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newUserEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Verify invitation exists by email
		_, err = testDB.InvitationByEmail(newUserEmail)
		c.Assert(err, qt.IsNil)

		// Delete the invitation by email
		err = testDB.DeleteInvitationByEmail(newUserEmail)
		c.Assert(err, qt.IsNil)

		// Verify invitation is deleted
		_, err = testDB.InvitationByEmail(newUserEmail)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})
}
