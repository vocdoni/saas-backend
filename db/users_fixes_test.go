package db

import (
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestAddUserToOrganization covers the atomic membership grant used by invitation
// acceptance: one entry per organization whatever the invited role, even under concurrency.
func TestAddUserToOrganization(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	// unknown user
	err := testDB.AddUserToOrganization("missing@user.test", testOrgAddress, ManagerRole)
	c.Assert(err, qt.Equals, ErrNotFound)

	userID, err := testDB.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.AddUserToOrganization(testUserEmail, testOrgAddress, ManagerRole), qt.IsNil)

	// a second grant for the same org — even with a different role — must conflict, not duplicate
	err = testDB.AddUserToOrganization(testUserEmail, testOrgAddress, AdminRole)
	c.Assert(err, qt.Equals, ErrAlreadyExists)
	user, err := testDB.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Organizations, qt.HasLen, 1)
	c.Assert(user.Organizations[0].Role, qt.Equals, ManagerRole)

	// concurrent grants to a second org: exactly one wins, the rest conflict, one entry results
	const grants = 8
	results := make([]error, grants)
	var wg sync.WaitGroup
	for i := range grants {
		wg.Go(func() {
			results[i] = testDB.AddUserToOrganization(testUserEmail, testAnotherOrgAddress, ViewerRole)
		})
	}
	wg.Wait()
	winners := 0
	for _, err := range results {
		if err == nil {
			winners++
		} else {
			c.Assert(err, qt.Equals, ErrAlreadyExists)
		}
	}
	c.Assert(winners, qt.Equals, 1)
	user, err = testDB.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Organizations, qt.HasLen, 2)
}
