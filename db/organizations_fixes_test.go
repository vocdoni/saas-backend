package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
)

// TestSetOrganizationUpdateDoesNotTouchMemberships covers the regression where every
// SetOrganization call re-added the creator as admin — resurrecting changed or removed
// roles — and deleted the whole organization when that failed.
func TestSetOrganizationUpdateDoesNotTouchMemberships(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	userID, err := testDB.SetUser(&User{
		Email:     testUserEmail,
		Password:  testUserPass,
		FirstName: testUserFirstName,
		LastName:  testUserLastName,
	})
	c.Assert(err, qt.IsNil)
	org := &Organization{Address: testOrgAddress, Creator: testUserEmail, CreatedAt: time.Now()}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	// creation granted the creator the admin role
	user, err := testDB.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Organizations, qt.HasLen, 1)
	c.Assert(user.Organizations[0].Role, qt.Equals, AdminRole)

	// demote the creator, then update the organization: the role must survive the update
	c.Assert(testDB.UpdateOrganizationUserRole(testOrgAddress, userID, ManagerRole), qt.IsNil)
	org.Country = "ES"
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	user, err = testDB.User(userID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Organizations, qt.HasLen, 1)
	c.Assert(user.Organizations[0].Role, qt.Equals, ManagerRole)

	// an update whose creator no longer resolves must neither fail nor delete the organization
	// (previously the failed membership grant deleted the stored organization)
	org.Creator = "ghost@missing.test"
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	stored, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Country, qt.Equals, "ES")

	// creating a fresh organization with a missing creator still fails and rolls back
	c.Assert(testDB.SetOrganization(&Organization{
		Address: testAnotherOrgAddress,
		Creator: "ghost@missing.test",
	}), qt.IsNotNil)
	_, err = testDB.Organization(testAnotherOrgAddress)
	c.Assert(err, qt.Equals, ErrNotFound)
}

// TestOrganizationSignerSeedImmutable covers the signer seed lifecycle: written on insert,
// never rewritten on update, with SignerSeedValue falling back to the creator for legacy rows.
func TestOrganizationSignerSeedImmutable(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	org := &Organization{Address: testOrgAddress, SignerSeed: "creator@seed.test"}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	stored, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.SignerSeed, qt.Equals, "creator@seed.test")

	// an update carrying a different seed must not overwrite the stored one
	stored.SignerSeed = "attacker@seed.test"
	stored.Country = "ES"
	c.Assert(testDB.SetOrganization(stored), qt.IsNil)
	stored, err = testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.SignerSeed, qt.Equals, "creator@seed.test")
	c.Assert(stored.Country, qt.Equals, "ES")
	c.Assert(stored.SignerSeedValue(), qt.Equals, "creator@seed.test")

	// a legacy row without a seed falls back to the creator email
	legacy := &Organization{Creator: "legacy@creator.test"}
	c.Assert(legacy.SignerSeedValue(), qt.Equals, "legacy@creator.test")
}

// TestOrgAddressesManagedBy covers the helper confining an API key's reach to the
// organizations its own organization manages.
func TestOrgAddressesManagedBy(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	integrator := testOrgAddress
	managed := testAnotherOrgAddress
	unrelated := testThirdOrgAddress
	c.Assert(testDB.SetOrganization(&Organization{Address: integrator}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: managed, ManagedBy: integrator}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: unrelated}), qt.IsNil)

	got, err := testDB.OrgAddressesManagedBy(integrator, []common.Address{managed, unrelated, testNonExistentOrg})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, []common.Address{managed})

	// no candidates or zero owner short-circuit to nothing
	got, err = testDB.OrgAddressesManagedBy(integrator, nil)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)
	got, err = testDB.OrgAddressesManagedBy(common.Address{}, []common.Address{managed})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)
}
