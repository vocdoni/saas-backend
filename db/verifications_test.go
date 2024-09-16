package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestUserVerificationCode(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)

	userID, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)

	_, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeAccountVerification)
	c.Assert(err, qt.Equals, ErrNotFound)

	testCode := "testCode"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeAccountVerification), qt.IsNil)

	code, err := db.UserVerificationCode(&User{ID: userID}, CodeTypeAccountVerification)
	c.Assert(err, qt.IsNil)
	c.Assert(code, qt.Equals, testCode)

	c.Assert(db.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
	_, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeAccountVerification)
	c.Assert(err, qt.Equals, ErrNotFound)
}

func TestSetVerificationCode(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)

	nonExistingUserID := uint64(100)
	c.Assert(db.SetVerificationCode(&User{ID: nonExistingUserID}, "testCode", CodeTypeAccountVerification), qt.Equals, ErrNotFound)

	userID, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)

	testCode := "testCode"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeAccountVerification), qt.IsNil)

	code, err := db.UserVerificationCode(&User{ID: userID}, CodeTypeAccountVerification)
	c.Assert(err, qt.IsNil)
	c.Assert(code, qt.Equals, testCode)

	testCode = "testCode2"
	c.Assert(db.SetVerificationCode(&User{ID: userID}, testCode, CodeTypeAccountVerification), qt.IsNil)

	code, err = db.UserVerificationCode(&User{ID: userID}, CodeTypeAccountVerification)
	c.Assert(err, qt.IsNil)
	c.Assert(code, qt.Equals, testCode)
}

func TestVerifyUser(t *testing.T) {
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()
	c := qt.New(t)

	nonExistingUserID := uint64(100)
	c.Assert(db.VerifyUserAccount(&User{ID: nonExistingUserID}), qt.Equals, ErrNotFound)

	userID, err := db.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)

	user, err := db.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Verified, qt.IsFalse)

	c.Assert(db.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
	user, err = db.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Verified, qt.IsTrue)
}
