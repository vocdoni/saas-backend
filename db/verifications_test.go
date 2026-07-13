package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

func TestVerifications(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	t.Run("TestUserVerificationCode", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail,
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		_, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.Equals, ErrNotFound)

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)

		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode)

		c.Assert(testDB.VerifyUserAccount(&User{ID: userID}), qt.IsNil)
		_, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.Equals, ErrNotFound)
	})

	t.Run("TestSetVerificationCode", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		nonExistingUserID := uint64(100)

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)

		err = testDB.SetVerificationCode(&User{ID: nonExistingUserID}, sealedCode, CodeTypeVerifyAccount, time.Now())
		c.Assert(err, qt.Equals, ErrNotFound)

		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "2",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode)
		c.Assert(code.Attempts, qt.Equals, 1)

		sealedCode2, err := internal.SealToken("testCode2", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode2, CodeTypeVerifyAccount, time.Now()), qt.IsNil)

		code, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode2)
	})

	t.Run("TestVerificationCodeIncrementAttempts", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "3",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode, CodeTypeVerifyAccount, time.Now()), qt.IsNil)
		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Attempts, qt.Equals, 1)

		c.Assert(testDB.VerificationCodeIncrementAttempts(sealedCode, CodeTypeVerifyAccount), qt.IsNil)
		code, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Attempts, qt.Equals, 2)
	})

	t.Run("TestVerificationCodeCheckAndAddAttempt", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		const maxAttempts = 3
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "4",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		user := &User{ID: userID}

		// no code yet: the guard reports ErrNotFound rather than a spurious lockout
		recorded, err := testDB.VerificationCodeCheckAndAddAttempt(user, CodeTypePasswordReset, maxAttempts)
		c.Assert(err, qt.Equals, ErrNotFound)
		c.Assert(recorded, qt.IsFalse)

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		// SetVerificationCode seeds Attempts at 1, so only maxAttempts-1 attempts are recordable
		c.Assert(testDB.SetVerificationCode(user, sealedCode, CodeTypePasswordReset, time.Now()), qt.IsNil)

		// record attempts until the cap is hit; each recorded call bumps the stored counter
		for want := 2; want <= maxAttempts; want++ {
			recorded, err = testDB.VerificationCodeCheckAndAddAttempt(user, CodeTypePasswordReset, maxAttempts)
			c.Assert(err, qt.IsNil)
			c.Assert(recorded, qt.IsTrue)
			code, err := testDB.UserVerificationCode(user, CodeTypePasswordReset)
			c.Assert(err, qt.IsNil)
			c.Assert(code.Attempts, qt.Equals, want)
		}

		// cap reached: further attempts fail closed and do not increment past the cap
		recorded, err = testDB.VerificationCodeCheckAndAddAttempt(user, CodeTypePasswordReset, maxAttempts)
		c.Assert(err, qt.IsNil)
		c.Assert(recorded, qt.IsFalse)
		code, err := testDB.UserVerificationCode(user, CodeTypePasswordReset)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Attempts, qt.Equals, maxAttempts)

		// a different code type for the same user is tracked independently (not locked)
		_, err = testDB.VerificationCodeCheckAndAddAttempt(user, CodeTypeVerifyAccount, maxAttempts)
		c.Assert(err, qt.Equals, ErrNotFound)
	})
}
