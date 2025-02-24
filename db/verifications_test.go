package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestUserVerificationCode(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	userID, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)

	_, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
	c.Assert(err, qt.Equals, ErrNotFound)

	testCode := "testCode"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

	code, err := db.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
	c.Assert(err, qt.IsNil)
	c.Assert(code.Code, qt.Equals, testCode)

	c.Assert(db.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
	_, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
	c.Assert(err, qt.Equals, ErrNotFound)
}

func TestSetVerificationCode(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	nonExistingUserID := uint64(100)
	err := db.SetVerificationCode(&User{ID: nonExistingUserID}, "testCode", CodeTypeVerifyAccount, time.Now())
	c.Assert(err, qt.Equals, ErrNotFound)

	userID, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)

	testCode := "testCode"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

	code, err := db.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
	c.Assert(err, qt.IsNil)
	c.Assert(code.Code, qt.Equals, testCode)

	testCode = "testCode2"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

	code, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
	c.Assert(err, qt.IsNil)
	c.Assert(code.Code, qt.Equals, testCode)
}
