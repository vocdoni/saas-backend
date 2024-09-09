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

func TestUserByEmail(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// test not found user
	user, err := db.UserByEmail(testUserEmail)
	c.Assert(user, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new user with the email
	_, err = db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)
	// test found user
	user, err = db.UserByEmail(testUserEmail)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, testUserEmail)
	c.Assert(user.Password, qt.Equals, testUserPass)
	c.Assert(user.FirstName, qt.Equals, testUserFirstName)
	c.Assert(user.LastName, qt.Equals, testUserLastName)
}

func TestUser(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// test not found user
	id := uint64(100)
	user, err := db.User(id)
	c.Assert(user, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new user with the ID
	_, err = db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)
	// get the user ID
	user, err = db.UserByEmail(testUserEmail)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	// test found user by ID
	user, err = db.User(user.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, testUserEmail)
	c.Assert(user.Password, qt.Equals, testUserPass)
	c.Assert(user.FirstName, qt.Equals, testUserFirstName)
	c.Assert(user.LastName, qt.Equals, testUserLastName)
}

func TestSetUser(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// trying to create a new user with invalid email
	user := &User{
		Email:     "invalid-email",
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	}
	_, err := db.SetUser(user)
	c.Assert(err, qt.IsNotNil)
	// trying to update a non existing user
	user.ID = 100
	_, err = db.SetUser(user)
	c.Assert(err, qt.Equals, ErrInvalidData)
	// unset the ID to create a new user
	user.ID = 0
	user.Email = testUserEmail
	// create a new user
	_, err = db.SetUser(user)
	c.Assert(err, qt.IsNil)
	// update the user
	newFirstName := "New User"
	user.FirstName = newFirstName
	_, err = db.SetUser(user)
	c.Assert(err, qt.IsNil)
	// get the user
	user, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, testUserEmail)
	c.Assert(user.Password, qt.Equals, testUserPass)
	c.Assert(user.FirstName, qt.Equals, newFirstName)
	c.Assert(user.LastName, qt.Equals, testUserLastName)
}

func TestDelUser(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// create a new user
	user := &User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	}
	_, err := db.SetUser(user)
	c.Assert(err, qt.IsNil)
	// get the user
	user, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	// delete the user by ID removing the email
	user.Email = ""
	c.Assert(db.DelUser(user), qt.IsNil)
	// restore the email and try to get the user
	user.Email = testUserEmail
	_, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.Equals, ErrNotFound)
	// insert the user again with the same email but no ID
	user.ID = 0
	_, err = db.SetUser(user)
	c.Assert(err, qt.IsNil)
	// delete the user by email
	c.Assert(db.DelUser(user), qt.IsNil)
	// try to get the user
	_, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.Equals, ErrNotFound)
}

func TestIsMemberOf(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)
	// create a new user with some organizations
	user := &User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
		Organizations: []OrganizationMember{
			{Address: "adminOrg", Role: AdminRole},
			{Address: "managerOrg", Role: ManagerRole},
			{Address: "viewOrg", Role: ViewerRole},
		},
	}
	_, err := db.SetUser(user)
	c.Assert(err, qt.IsNil)
	// test the user is member of the organizations
	for _, org := range user.Organizations {
		success, err := db.IsMemberOf(user.Email, org.Address, org.Role)
		c.Assert(err, qt.IsNil)
		c.Assert(success, qt.IsTrue)
	}
	// test the user is not member of the organizations
	success, err := db.IsMemberOf(user.Email, "notFoundOrg", AdminRole)
	c.Assert(err, qt.Equals, ErrNotFound)
	c.Assert(success, qt.IsFalse)
	// test the user has no role in the organization
	success, err = db.IsMemberOf(user.Email, "adminOrg", ViewerRole)
	c.Assert(err, qt.IsNil)
	c.Assert(success, qt.IsFalse)
	// test not found user
	success, err = db.IsMemberOf("notFoundUser", "adminOrg", AdminRole)
	c.Assert(err, qt.Equals, ErrNotFound)
	c.Assert(success, qt.IsFalse)
}
