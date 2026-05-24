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

	t.Run("TestSetVerificationCodeExpirationBoundary", func(_ *testing.T) {
		// Documenting current behavior: no prior commit has been identified where this
		// assertion would fail. SetVerificationCode has always used ReplaceOne (upsert),
		// which atomically overwrites the document, and there is no MongoDB TTL index nor
		// application-level expiration check at query time. This test guards against
		// future regressions if either of those invariants changes.
		t.Log("documenting current behavior (not a regression for a fixed bug): " +
			"SetVerificationCode atomically replaces the document via ReplaceOne; " +
			"no TTL index exists and expiration is not enforced at query time, " +
			"so the new code always wins with no observable race on current main")
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
		userID, err := testDB.SetUser(&User{
			Email:     testUserEmail + "4",
			Password:  testUserPass,
			FirstName: testUserFirstName,
			LastName:  testUserLastName,
		})
		c.Assert(err, qt.IsNil)

		sealedCode1, err := internal.SealToken("oldCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		// Set a code that expires almost immediately, simulating the expiration boundary.
		oldExp := time.Now().Add(time.Millisecond)
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode1, CodeTypeVerifyAccount, oldExp), qt.IsNil)

		// Wait past the old code's expiration boundary.
		time.Sleep(2 * time.Millisecond)

		// Set a new code at (or just after) the old code's expiration boundary.
		sealedCode2, err := internal.SealToken("newCode", testUserEmail, "mock-app-secret")
		c.Assert(err, qt.IsNil)
		newExp := time.Now().Add(time.Hour)
		c.Assert(testDB.SetVerificationCode(&User{ID: userID}, sealedCode2, CodeTypeVerifyAccount, newExp), qt.IsNil)

		// The new code must win: UserVerificationCode must return the new sealed code and a future expiration.
		code, err := testDB.UserVerificationCode(&User{ID: userID}, CodeTypeVerifyAccount)
		c.Assert(err, qt.IsNil)
		c.Assert(code.SealedCode, qt.DeepEquals, sealedCode2)
		c.Assert(code.Expiration.After(time.Now()), qt.IsTrue)
	})
}
