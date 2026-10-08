package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

// testVerification is a helper to build a UserVerification for the tests.
func testVerification(userID uint64, sealedCode []byte, t CodeType, exp time.Time) *UserVerification {
	return &UserVerification{
		UserID:     userID,
		SealedCode: sealedCode,
		Type:       t,
		Expiration: exp,
	}
}

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

		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode, CodeTypeVerifyAccount, time.Now())), qt.IsNil)

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

		err = testDB.SetVerificationCode(testVerification(nonExistingUserID, sealedCode, CodeTypeVerifyAccount, time.Now()))
		c.Assert(err, qt.Equals, ErrNotFound)

		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "2",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode, CodeTypeVerifyAccount, time.Now())), qt.IsNil)

		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode)
		// guesses start at zero; the initial delivery counts as the first send
		c.Assert(code.Attempts, qt.Equals, 0)
		c.Assert(code.Sends, qt.Equals, 1)

		sealedCode2, err := internal.SealToken("testCode2", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode2, CodeTypeVerifyAccount, time.Now())), qt.IsNil)

		code, err = testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode2)
	})

	t.Run("TestCodesPerUserAndType", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "5",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		user := &User{ID: userID}

		verifyCode, err := internal.SealToken("verifyCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		resetCode, err := internal.SealToken("resetCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)

		// a password-reset code must not replace a pending account-verification code
		exp := time.Now().Add(time.Hour)
		c.Assert(testDB.SetVerificationCode(testVerification(userID, verifyCode, CodeTypeVerifyAccount, exp)), qt.IsNil)
		c.Assert(testDB.SetVerificationCode(testVerification(userID, resetCode, CodeTypePasswordReset, exp)), qt.IsNil)

		gotVerify, err := testDB.UserVerificationCode(user, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(gotVerify.SealedCode, qt.DeepEquals, verifyCode)
		gotReset, err := testDB.UserVerificationCode(user, CodeTypePasswordReset)
		c.Assert(err, qt.IsNil)
		c.Assert(gotReset.SealedCode, qt.DeepEquals, resetCode)

		// consuming one type leaves the other untouched
		c.Assert(testDB.ConsumeVerificationCode(user, CodeTypePasswordReset, resetCode), qt.IsNil)
		_, err = testDB.UserVerificationCode(user, CodeTypePasswordReset)
		c.Assert(err, qt.Equals, ErrNotFound)
		_, err = testDB.UserVerificationCode(user, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
	})

	t.Run("TestConsumeVerificationCode", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "6",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		user := &User{ID: userID}

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode, CodeTypePasswordReset, time.Now())), qt.IsNil)

		// a different sealed code must not consume the stored one
		other, err := internal.SealToken("otherCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.ConsumeVerificationCode(user, CodeTypePasswordReset, other), qt.Equals, ErrNotFound)

		// the exact code is consumed exactly once
		c.Assert(testDB.ConsumeVerificationCode(user, CodeTypePasswordReset, sealedCode), qt.IsNil)
		c.Assert(testDB.ConsumeVerificationCode(user, CodeTypePasswordReset, sealedCode), qt.Equals, ErrNotFound)
	})

	t.Run("TestVerificationCodeTrySend", func(_ *testing.T) {
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		const maxSends = 3
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "3",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)
		user := &User{ID: userID}

		// no code yet: the guard reports ErrNotFound
		_, err = testDB.VerificationCodeTrySend(user, CodeTypeVerifyAccount, 0, maxSends)
		c.Assert(err, qt.Equals, ErrNotFound)

		sealedCode, err := internal.SealToken("testCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode, CodeTypeVerifyAccount, time.Now())), qt.IsNil)

		// the cooldown blocks an immediate resend
		sent, err := testDB.VerificationCodeTrySend(user, CodeTypeVerifyAccount, time.Hour, maxSends)
		c.Assert(err, qt.IsNil)
		c.Assert(sent, qt.IsFalse)

		// without cooldown, resends are allowed until the cap (initial delivery counts as one)
		for want := 2; want <= maxSends; want++ {
			sent, err = testDB.VerificationCodeTrySend(user, CodeTypeVerifyAccount, 0, maxSends)
			c.Assert(err, qt.IsNil)
			c.Assert(sent, qt.IsTrue)
			code, err := testDB.UserVerificationCode(user, CodeTypeVerifyAccount)
			c.Assert(err, qt.IsNil)
			c.Assert(code.Sends, qt.Equals, want)
		}
		sent, err = testDB.VerificationCodeTrySend(user, CodeTypeVerifyAccount, 0, maxSends)
		c.Assert(err, qt.IsNil)
		c.Assert(sent, qt.IsFalse)

		// resending never consumes guess budget
		code, err := testDB.UserVerificationCode(user, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.Attempts, qt.Equals, 0)
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
		// guesses start at zero, so exactly maxAttempts attempts are recordable
		c.Assert(testDB.SetVerificationCode(testVerification(userID, sealedCode, CodeTypePasswordReset, time.Now())), qt.IsNil)

		// record attempts until the cap is hit; each recorded call bumps the stored counter
		for want := 1; want <= maxAttempts; want++ {
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
