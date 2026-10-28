package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
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
		nonExistentID, err := bson.ObjectIDFromHex("5f50cf9b8b5cf3b5e1c8a1a1")
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

// TestDeleteInvitationsByInviter covers DeleteInvitationsByInviter: it removes only the pending invites
// of the given org issued by the given user, leaving the same user's invites in other orgs and other
// inviters' invites in the same org untouched.
func TestDeleteInvitationsByInviter(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa1000000000000000000000000000000000000a1")
	orgB := common.HexToAddress("0xb1000000000000000000000000000000000000b1")
	seedOrg(t, orgA)
	seedOrg(t, orgB)
	// inviter1 is admin of both orgs, inviter2 only of orgA
	inviter1, err := testDB.SetUser(&User{
		Email: "inviter1@x.test", Password: "p", FirstName: "I", LastName: "1",
		Organizations: []OrganizationUser{{Address: orgA, Role: AdminRole}, {Address: orgB, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)
	inviter2, err := testDB.SetUser(&User{
		Email: "inviter2@x.test", Password: "p", FirstName: "I", LastName: "2",
		Organizations: []OrganizationUser{{Address: orgA, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)
	expiration := time.Now().Add(time.Hour)

	invites := []struct {
		org     common.Address
		inviter uint64
		email   string
		code    string
	}{
		{orgA, inviter1, "a1@x.test", "revokeA1x"},
		{orgA, inviter1, "a2@x.test", "revokeA2x"},
		{orgA, inviter2, "a3@x.test", "revokeA3x"},
		{orgB, inviter1, "b1@x.test", "revokeB1x"},
	}
	for _, in := range invites {
		c.Assert(testDB.CreateInvitation(&OrganizationInvite{
			OrganizationAddress: in.org,
			CurrentUserID:       in.inviter,
			NewUserEmail:        in.email,
			Role:                ViewerRole,
			Expiration:          expiration,
			InvitationCode:      in.code,
		}), qt.IsNil)
	}

	n, err := testDB.DeleteInvitationsByInviter(orgA, inviter1)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	// orgA keeps inviter2's invite, orgB keeps inviter1's invite to it
	aPending, err := testDB.PendingInvitations(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(aPending, qt.HasLen, 1)
	c.Assert(aPending[0].CurrentUserID, qt.Equals, inviter2)
	bPending, err := testDB.PendingInvitations(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(bPending, qt.HasLen, 1)

	// nothing left to delete for that inviter in orgA
	n, err = testDB.DeleteInvitationsByInviter(orgA, inviter1)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))

	_, err = testDB.DeleteInvitationsByInviter(common.Address{}, inviter1)
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestRevokeInvitationByCode covers RevokeInvitationByCode: only the call that deletes the invitation reports true.
func TestRevokeInvitationByCode(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa3000000000000000000000000000000000000a3")
	seedOrg(t, orgA)
	inviter, err := testDB.SetUser(&User{
		Email: "inviter-revoke@x.test", Password: "p", FirstName: "I", LastName: "R",
		Organizations: []OrganizationUser{{Address: orgA, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		OrganizationAddress: orgA,
		CurrentUserID:       inviter,
		NewUserEmail:        "revoke@x.test",
		Role:                ViewerRole,
		Expiration:          time.Now().Add(time.Hour),
		InvitationCode:      "revokeOnce",
	}), qt.IsNil)

	revoked, err := testDB.RevokeInvitationByCode("revokeOnce")
	c.Assert(err, qt.IsNil)
	c.Assert(revoked, qt.IsTrue)

	// a second revocation of the same code, or of an unknown one, deletes nothing
	revoked, err = testDB.RevokeInvitationByCode("revokeOnce")
	c.Assert(err, qt.IsNil)
	c.Assert(revoked, qt.IsFalse)
	revoked, err = testDB.RevokeInvitationByCode("unknownCode")
	c.Assert(err, qt.IsNil)
	c.Assert(revoked, qt.IsFalse)
}

// TestCountOrgWriters: users holding admin or manager in the org plus the non-expired invitations
// to one; viewers, expired invitations and other organizations hold no seat.
func TestCountOrgWriters(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	other := common.Address{0x42}
	for i, ou := range []OrganizationUser{
		{Address: testOrgAddress, Role: AdminRole},
		{Address: testOrgAddress, Role: ManagerRole},
		{Address: testOrgAddress, Role: ViewerRole},
		{Address: other, Role: AdminRole},
	} {
		_, err := testDB.SetUser(&User{
			Email: fmt.Sprintf("writer%d@example.com", i), Password: testUserPass, Organizations: []OrganizationUser{ou},
		})
		c.Assert(err, qt.IsNil)
	}
	for _, inv := range []OrganizationInvite{
		{OrganizationAddress: testOrgAddress, Role: ManagerRole, Expiration: time.Now().Add(time.Hour)},
		{OrganizationAddress: testOrgAddress, Role: ViewerRole, Expiration: time.Now().Add(time.Hour)},
		{OrganizationAddress: testOrgAddress, Role: AdminRole, Expiration: time.Now().Add(-time.Hour)},
		{OrganizationAddress: other, Role: AdminRole, Expiration: time.Now().Add(time.Hour)},
	} {
		inv.ID, inv.CurrentUserID = bson.NewObjectID(), currentUserID
		inv.NewUserEmail, inv.InvitationCode = fmt.Sprintf("inv%s@example.com", inv.ID.Hex()), "code"+inv.ID.Hex()
		_, err := testDB.organizationInvites.InsertOne(context.Background(), inv)
		c.Assert(err, qt.IsNil)
	}
	n, err := testDB.CountOrgWriters(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(3))
}
