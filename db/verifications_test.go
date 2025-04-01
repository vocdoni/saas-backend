package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestVerifications(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("TestUserVerificationCode", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.Equals, ErrNotFound)

		testCode := "testCode"
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Code, qt.Equals, testCode)

		c.Assert(testDB.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
		_, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.Equals, ErrNotFound)
	})

	t.Run("TestSetVerificationCode", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		nonExistingUserID := uint64(100)
		err := testDB.SetVerificationCode(&User{ID: nonExistingUserID}, "testCode", CodeTypeVerifyAccount, time.Now())
		c.Assert(err, qt.Equals, ErrNotFound)

		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "2",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		testCode := "testCode"
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Code, qt.Equals, testCode)

		testCode = "testCode2"
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Code, qt.Equals, testCode)
	})
}
