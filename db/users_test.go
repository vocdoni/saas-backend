package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

const (
	testUserEmail     = "user@email.test"
	testUserPass      = "testPassword"
	testUserFirstName = "User"
	testUserLastName  = "Name"
)

func TestUsers(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	t.Run("UserByEmail", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// test not found user
		user, err := testDB.UserByEmail(testDBUserEmail)
		c.Assert(user, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new user with the email
		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		// test found user
		user, err = testDB.UserByEmail(testUserEmail)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Email, qt.Equals, testUserEmail)
		c.Assert(user.Password, qt.Equals, testUserPass)
		c.Assert(user.FirstName, qt.Equals, testUserFirstName)
		c.Assert(user.LastName, qt.Equals, testUserLastName)
		c.Assert(user.Verified, qt.IsFalse)
	})

	t.Run("UserByID", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// test not found user
		id := uint64(100)
		user, err := testDB.User(id)
		c.Assert(user, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
		// create a new user with the ID
		_, err = testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		// get the user ID
		user, err = testDB.UserByEmail(testUserEmail)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		// test found user by ID
		user, err = testDB.User(user.ID)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Email, qt.Equals, testUserEmail)
		c.Assert(user.Password, qt.Equals, testUserPass)
		c.Assert(user.FirstName, qt.Equals, testUserFirstName)
		c.Assert(user.LastName, qt.Equals, testUserLastName)
		c.Assert(user.Verified, qt.IsFalse)
	})

	t.Run("SetUser", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// trying to create a new user with invalid email
		user := &User{
			Email:     "invalid-email",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		}
		_, err := testDB.SetUser(user)
		c.Assert(err, qt.IsNotNil)
		// trying to update a non existing user
		user.ID = 100
		_, err = testDB.SetUser(user)
		c.Assert(err, qt.Equals, ErrInvalidData)
		// unset the ID to create a new user
		user.ID = 0
		user.Email = testUserEmail
		// create a new user
		_, err = testDB.SetUser(user)
		c.Assert(err, qt.IsNil)
		// update the user
		newFirstName := "New User"
		user.FirstName = newFirstName
		_, err = testDB.SetUser(user)
		c.Assert(err, qt.IsNil)
		// get the user
		user, err = testDB.UserByEmail(user.Email)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		c.Assert(user.Email, qt.Equals, testUserEmail)
		c.Assert(user.Password, qt.Equals, testUserPass)
		c.Assert(user.FirstName, qt.Equals, newFirstName)
		c.Assert(user.LastName, qt.Equals, testUserLastName)
		c.Assert(user.Verified, qt.IsFalse)
	})

	t.Run("DeleteUser", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new user
		user := &User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		}
		_, err := testDB.SetUser(user)
		c.Assert(err, qt.IsNil)
		// get the user
		user, err = testDB.UserByEmail(user.Email)
		c.Assert(err, qt.IsNil)
		c.Assert(user, qt.Not(qt.IsNil))
		// delete the user by ID removing the email
		user.Email = ""
		c.Assert(testDB.DelUser(user), qt.IsNil)
		// restore the email and try to get the user
		user.Email = testUserEmail
		_, err = testDB.UserByEmail(user.Email)
		c.Assert(err, qt.Equals, ErrNotFound)
		// insert the user again with the same email but no ID
		user.ID = 0
		_, err = testDB.SetUser(user)
		c.Assert(err, qt.IsNil)
		// delete the user by email
		c.Assert(testDB.DelUser(user), qt.IsNil)
		// try to get the user
		_, err = testDB.UserByEmail(user.Email)
		c.Assert(err, qt.Equals, ErrNotFound)
	})

	t.Run("UserHasRoleInOrg", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		// create a new user with some organizations
		user := &User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
				{Address: testAnotherOrgAddress, Role: ManagerRole},
				{Address: testThirdOrgAddress, Role: ViewerRole},
			},
		}
		_, err := testDB.SetUser(user)
		c.Assert(err, qt.IsNil)
		// test the user has a role in the organizations
		for _, org := range user.Organizations {
			success, err := testDB.UserHasRoleInOrg(user.Email, org.Address, org.Role)
			c.Assert(err, qt.IsNil)
			c.Assert(success, qt.IsTrue)
			success, err = testDB.UserHasAnyRoleInOrg(user.Email, org.Address)
			c.Assert(err, qt.IsNil)
			c.Assert(success, qt.IsTrue)
		}
		// test the user role in a non-existent organizations
		success, err := testDB.UserHasRoleInOrg(user.Email, testNonExistentOrg, AdminRole)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(success, qt.IsFalse)
		// test the user with a different role in the organization
		success, err = testDB.UserHasRoleInOrg(user.Email, testOrgAddress, ViewerRole)
		c.Assert(err, qt.IsNil)
		c.Assert(success, qt.IsFalse)
		// test not found user
		success, err = testDB.UserHasRoleInOrg("notFoundUser", testOrgAddress, AdminRole)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(success, qt.IsFalse)
		// test no role
		success, err = testDB.UserHasAnyRoleInOrg(user.Email, testFourthOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(success, qt.IsFalse)
	})

	t.Run("FieldSpecificUpdates", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
			Organizations: []OrganizationUser{
				{Address: testOrgAddress, Role: AdminRole},
			},
		})
		c.Assert(err, qt.IsNil)

		// UpdateUserProfile only writes the names, never memberships/password/email
		c.Assert(testDB.UpdateUserProfile(userID, "NewFirst", ""), qt.IsNil)
		user, err := testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.FirstName, qt.Equals, "NewFirst")
		c.Assert(user.LastName, qt.Equals, testUserLastName)
		c.Assert(user.Email, qt.Equals, testUserEmail)
		c.Assert(user.Password, qt.Equals, testUserPass)
		c.Assert(user.Organizations, qt.HasLen, 1)
		c.Assert(user.SessionVersion, qt.Equals, uint64(0))

		// UpdateUserPassword writes only the password and bumps the session version
		c.Assert(testDB.UpdateUserPassword(userID, "newHash"), qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.Password, qt.Equals, "newHash")
		c.Assert(user.SessionVersion, qt.Equals, uint64(1))

		// UpdateUserEmail writes only the email and bumps the session version
		c.Assert(testDB.UpdateUserEmail(userID, "renamed@email.test"), qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.Email, qt.Equals, "renamed@email.test")
		c.Assert(user.SessionVersion, qt.Equals, uint64(2))

		// UpdateUserEmail refuses an email already bound to another user
		otherID, err := testDB.SetUser(&User{
			Email:     "other@email.test",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.UpdateUserEmail(otherID, "renamed@email.test"), qt.Equals, ErrAlreadyExists)

		// OAuth link/unlink touch only the single provider entry
		c.Assert(testDB.SetUserOAuthProvider(userID, "github", OAuthProvider{ExternalID: "0xabc"}), qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.OAuth["github"].ExternalID, qt.Equals, "0xabc")
		c.Assert(user.SessionVersion, qt.Equals, uint64(2))
		c.Assert(testDB.DeleteUserOAuthProvider(userID, "github"), qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		_, linked := user.OAuth["github"]
		c.Assert(linked, qt.IsFalse)

		// SetUser never writes the session version, so a stale snapshot cannot
		// resurrect revoked sessions
		stale := *user
		stale.SessionVersion = 0
		_, err = testDB.SetUser(&stale)
		c.Assert(err, qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.SessionVersion, qt.Equals, uint64(2))

		// unknown user yields ErrNotFound
		c.Assert(testDB.UpdateUserPassword(99999, "x"), qt.Equals, ErrNotFound)
	})

	t.Run("VerifyUser", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

		nonExistingUserID := uint64(100)
		c.Assert(testDB.VerifyUserAccount(&User{ID: nonExistingUserID}), qt.Equals, ErrNotFound)

		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		user, err := testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.Verified, qt.IsFalse)

		c.Assert(testDB.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
		user, err = testDB.User(userID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.Verified, qt.IsTrue)
	})
}
