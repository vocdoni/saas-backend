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
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })
	t.Run("UserByEmail", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

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
		c.Assert(testDB.Reset(), qt.IsNil)

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
		c.Assert(testDB.Reset(), qt.IsNil)

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
		c.Assert(testDB.Reset(), qt.IsNil)

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
		c.Assert(testDB.Reset(), qt.IsNil)

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

	t.Run("VerifyUser", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)

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
