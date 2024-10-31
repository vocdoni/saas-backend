package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

var (
	invitationCode = "abc123"
	orgAddress     = "0x1234567890"
	currentUserID  = uint64(1)
	newMemberEmail = "inviteme@email.com"
	expires        = time.Now().Add(time.Hour)
)

func TestCreateInvitation(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	// non existing organization
	testInvite := &OrganizationInvite{
		InvitationCode:      invitationCode,
		OrganizationAddress: orgAddress,
		CurrentUserID:       currentUserID,
		NewUserEmail:        newMemberEmail,
		Role:                AdminRole,
		Expiration:          expires,
	}
	c.Assert(db.CreateInvitation(testInvite), qt.ErrorIs, ErrNotFound)
	// non existing user
	c.Assert(db.SetOrganization(&Organization{
		Address: orgAddress,
	}), qt.IsNil)
	c.Assert(db.CreateInvitation(testInvite), qt.ErrorIs, ErrNotFound)
	// non organization member
	_, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.CreateInvitation(testInvite).Error(), qt.Equals, "user is not part of the organization")
	// expiration date in the past
	_, err = db.SetUser(&User{
		ID: currentUserID,
		Organizations: []OrganizationMember{
			{Address: orgAddress, Role: AdminRole},
		},
	})
	c.Assert(err, qt.IsNil)
	testInvite.Expiration = time.Now().Add(-time.Hour)
	c.Assert(db.CreateInvitation(testInvite).Error(), qt.Equals, "expiration date must be in the future")
	// invalid role
	testInvite.Expiration = expires
	testInvite.Role = "invalid"
	c.Assert(db.CreateInvitation(testInvite).Error(), qt.Equals, "invalid role")
	// invitation expires
	testInvite.Role = AdminRole
	testInvite.Expiration = time.Now().Add(time.Second)
	c.Assert(db.CreateInvitation(testInvite), qt.IsNil)
	// TTL index could take up to 1 minute
	time.Sleep(time.Second * 75)
	_, err = db.Invitation(invitationCode)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	// success
	testInvite.Expiration = expires
	c.Assert(db.CreateInvitation(testInvite), qt.IsNil)
}

func TestInvitation(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	_, err := db.Invitation(invitationCode)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	c.Assert(db.SetOrganization(&Organization{
		Address: orgAddress,
	}), qt.IsNil)
	_, err = db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
		Organizations: []OrganizationMember{
			{Address: orgAddress, Role: AdminRole},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.CreateInvitation(&OrganizationInvite{
		InvitationCode:      invitationCode,
		OrganizationAddress: orgAddress,
		CurrentUserID:       currentUserID,
		NewUserEmail:        newMemberEmail,
		Role:                AdminRole,
		Expiration:          expires,
	}), qt.IsNil)
	invitation, err := db.Invitation(invitationCode)
	c.Assert(err, qt.IsNil)
	c.Assert(invitation.InvitationCode, qt.Equals, invitationCode)
	c.Assert(invitation.OrganizationAddress, qt.Equals, orgAddress)
	c.Assert(invitation.CurrentUserID, qt.Equals, currentUserID)
	c.Assert(invitation.NewUserEmail, qt.Equals, newMemberEmail)
	c.Assert(invitation.Role, qt.Equals, AdminRole)
	// truncate expiration to seconds to avoid rounding issues, also set to UTC
	c.Assert(invitation.Expiration.Truncate(time.Second).UTC(), qt.Equals, expires.Truncate(time.Second).UTC())
}

func TestDeleteInvitation(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// non existing invitation does not return an error on delete attempt
	c.Assert(db.DeleteInvitation(invitationCode), qt.IsNil)
	// create valid invitation
	c.Assert(db.SetOrganization(&Organization{
		Address: orgAddress,
	}), qt.IsNil)
	_, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
		Organizations: []OrganizationMember{
			{Address: orgAddress, Role: AdminRole},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(db.CreateInvitation(&OrganizationInvite{
		InvitationCode:      invitationCode,
		OrganizationAddress: orgAddress,
		CurrentUserID:       currentUserID,
		NewUserEmail:        newMemberEmail,
		Role:                AdminRole,
		Expiration:          expires,
	}), qt.IsNil)
	_, err = db.Invitation(invitationCode)
	c.Assert(err, qt.IsNil)
	// delete the invitation
	c.Assert(db.DeleteInvitation(invitationCode), qt.IsNil)
	_, err = db.Invitation(invitationCode)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
}
