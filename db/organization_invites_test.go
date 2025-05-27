package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

const (
	currentUserID = uint64(1)
)

func TestOrganizationInvites(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
	expires := time.Now().Add(time.Hour)

	t.Run("GetInvitation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test getting non-existent invitation
		_, err := testDB.Invitation(invitationCode)
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
			Organizations: []OrganizationMember{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newMemberEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Get invitation
		invitation, err := testDB.Invitation(invitationCode)
		c.Assert(err, qt.IsNil)
		c.Assert(invitation.InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitation.OrganizationAddress, qt.Equals, testOrgAddress)
		c.Assert(invitation.CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitation.NewUserEmail, qt.Equals, newMemberEmail)
		c.Assert(invitation.Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())
	})

	t.Run("PendingInvitations", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
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
			Organizations: []OrganizationMember{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newMemberEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// List invitations expecting one
		invitations, err = testDB.PendingInvitations(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(invitations, qt.HasLen, 1)
		c.Assert(invitations[0].InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitations[0].OrganizationAddress, qt.Equals, testOrgAddress)
		c.Assert(invitations[0].CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitations[0].NewUserEmail, qt.Equals, newMemberEmail)
		c.Assert(invitations[0].Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitations[0].Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())

		// Delete the invitation
		err = testDB.DeleteInvitation(invitationCode)
		c.Assert(err, qt.IsNil)

		// List invitations expecting none
		invitations, err = testDB.PendingInvitations(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(invitations, qt.HasLen, 0)
	})

	t.Run("DeleteInvitation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Non existing invitation does not return an error on delete attempt
		err := testDB.DeleteInvitation(invitationCode)
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
			Organizations: []OrganizationMember{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newMemberEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Verify invitation exists
		_, err = testDB.Invitation(invitationCode)
		c.Assert(err, qt.IsNil)

		// Delete the invitation
		err = testDB.DeleteInvitation(invitationCode)
		c.Assert(err, qt.IsNil)

		// Verify invitation is deleted
		_, err = testDB.Invitation(invitationCode)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})

	t.Run("InvitationByEmail", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test getting non-existent invitation by email
		_, err := testDB.InvitationByEmail(newMemberEmail)
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
			Organizations: []OrganizationMember{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newMemberEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Get invitation by email
		invitation, err := testDB.InvitationByEmail(newMemberEmail)
		c.Assert(err, qt.IsNil)
		c.Assert(invitation.InvitationCode, qt.Equals, invitationCode)
		c.Assert(invitation.OrganizationAddress, qt.Equals, testOrgAddress)
		c.Assert(invitation.CurrentUserID, qt.Equals, currentUserID)
		c.Assert(invitation.NewUserEmail, qt.Equals, newMemberEmail)
		c.Assert(invitation.Role, qt.Equals, AdminRole)
		// Truncate expiration to seconds to avoid rounding issues, also set to UTC
		c.Assert(invitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())
	})

	t.Run("DeleteInvitationByEmail", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Non existing invitation does not return an error on delete attempt
		err := testDB.DeleteInvitationByEmail(newMemberEmail)
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
			Organizations: []OrganizationMember{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// Create invitation
		err = testDB.CreateInvitation(&OrganizationInvite{
			InvitationCode:      invitationCode,
			OrganizationAddress: testOrgAddress,
			CurrentUserID:       currentUserID,
			NewUserEmail:        newMemberEmail,
			Role:                AdminRole,
			Expiration:          expires,
		})
		c.Assert(err, qt.IsNil)

		// Verify invitation exists by email
		_, err = testDB.InvitationByEmail(newMemberEmail)
		c.Assert(err, qt.IsNil)

		// Delete the invitation by email
		err = testDB.DeleteInvitationByEmail(newMemberEmail)
		c.Assert(err, qt.IsNil)

		// Verify invitation is deleted
		_, err = testDB.InvitationByEmail(newMemberEmail)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})
}
