package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestUserByEmail(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// test not found user
	email := "user@email.test"
	user, err := db.UserByEmail(email)
	c.Assert(user, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new user with the email
	password := "password"
	fullName := "User Name"
	c.Assert(db.SetUser(&User{
		Email:    email,
		Password: password,
		FullName: fullName,
	}), qt.IsNil)
	// test found user
	user, err = db.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, email)
	c.Assert(user.Password, qt.Equals, password)
	c.Assert(user.FullName, qt.Equals, fullName)
}

func TestUser(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// test not found user
	id := uint64(100)
	user, err := db.User(id)
	c.Assert(user, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
	// create a new user with the ID
	email := "user@email.test"
	password := "password"
	fullName := "User Name"
	c.Assert(db.SetUser(&User{
		Email:    email,
		Password: password,
		FullName: fullName,
	}), qt.IsNil)
	// get the user ID
	user, err = db.UserByEmail(email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	// test found user by ID
	user, err = db.User(user.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, email)
	c.Assert(user.Password, qt.Equals, password)
	c.Assert(user.FullName, qt.Equals, fullName)
}

func TestSetUser(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// trying to create a new user with invalid email
	user := &User{
		Email:    "invalid-email",
		Password: "password",
		FullName: "User Name",
	}
	c.Assert(db.SetUser(user), qt.IsNotNil)
	// trying to update a non existing user
	user.ID = 100
	c.Assert(db.SetUser(user), qt.Equals, ErrInvalidData)
	// unset the ID to create a new user
	user.ID = 0
	user.Email = "user@email.test"
	// create a new user
	c.Assert(db.SetUser(user), qt.IsNil)
	// update the user
	user.FullName = "New User Name"
	c.Assert(db.SetUser(user), qt.IsNil)
	// get the user
	user, err := db.UserByEmail(user.Email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	c.Assert(user.Email, qt.Equals, user.Email)
	c.Assert(user.Password, qt.Equals, user.Password)
	c.Assert(user.FullName, qt.Equals, user.FullName)
}

func TestDelUser(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// create a new user
	user := &User{
		Email:    "user@email.test",
		Password: "password",
		FullName: "User Name",
	}
	c.Assert(db.SetUser(user), qt.IsNil)
	// get the user
	user, err := db.UserByEmail(user.Email)
	c.Assert(err, qt.IsNil)
	c.Assert(user, qt.Not(qt.IsNil))
	// delete the user by ID removing the email
	user.Email = ""
	c.Assert(db.DelUser(user), qt.IsNil)
	// restore the email and try to get the user
	user.Email = "user@email.test"
	_, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.Equals, ErrNotFound)
	// insert the user again with the same email but no ID
	user.ID = 0
	c.Assert(db.SetUser(user), qt.IsNil)
	// delete the user by email
	c.Assert(db.DelUser(user), qt.IsNil)
	// try to get the user
	_, err = db.UserByEmail(user.Email)
	c.Assert(err, qt.Equals, ErrNotFound)
}

func TestIsMemberOf(t *testing.T) {
	defer db.Reset()
	c := qt.New(t)
	// create a new user with some organizations
	user := &User{
		Email:    "user@email.test",
		Password: "password",
		Organizations: []OrganizationMember{
			{Address: "adminOrg", Role: AdminRole},
			{Address: "managerOrg", Role: ManagerRole},
			{Address: "viewOrg", Role: ViewerRole},
		},
	}
	c.Assert(db.SetUser(user), qt.IsNil)
	// test the user is member of the organizations
	for _, org := range user.Organizations {
		success, err := db.IsMemberOf(user.Email, org.Address, org.Role)
		c.Assert(err, qt.IsNil)
		c.Assert(success, qt.IsTrue)
	}
	// test the user is not member of the organizations
	success, err := db.IsMemberOf(user.Email, "notFoundOrg", AdminRole)
	c.Assert(err, qt.IsNil)
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
