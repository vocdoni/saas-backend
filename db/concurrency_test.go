package db

import (
	"context"
	"fmt"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
)

// secondReplica opens a second MongoStorage handle on the same database, so these
// tests exercise the cross-replica path where the in-process keysLock offers no
// protection at all.
func secondReplica(c *qt.C) *MongoStorage {
	replica, err := New(mongoURI, testDB.database)
	c.Assert(err, qt.IsNil)
	c.Cleanup(replica.Close)
	return replica
}

// TestConcurrentSetUserInserts pins that concurrent user inserts — also through a
// second connection acting as another service replica — are each handed a unique
// ID by the atomic counter instead of racing on a read-max-plus-one.
func TestConcurrentSetUserInserts(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	replica := secondReplica(c)
	handles := []*MongoStorage{testDB, replica}

	// an existing user seeds the lazy counter above its ID
	firstID, err := testDB.SetUser(&User{Email: "seed@test.com", Password: testDBUserPass})
	c.Assert(err, qt.IsNil)

	const n = 20
	ids := make([]uint64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ids[i], errs[i] = handles[i%2].SetUser(&User{
				Email:    fmt.Sprintf("concurrent%d@test.com", i),
				Password: testDBUserPass,
			})
		})
	}
	wg.Wait()

	seen := map[uint64]bool{firstID: true}
	for i := range n {
		c.Assert(errs[i], qt.IsNil)
		c.Assert(seen[ids[i]], qt.IsFalse, qt.Commentf("user id %d handed out twice", ids[i]))
		seen[ids[i]] = true
	}
}

// TestConcurrentConsumeCSPProcess pins that the vote overwrite budget holds under
// concurrency across replicas: exactly MaxVoteOverwritesPerProcess+1 consumptions
// succeed, every further one is refused as already consumed.
func TestConcurrentConsumeCSPProcess(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	replica := secondReplica(c)
	handles := []*MongoStorage{testDB, replica}

	token := internal.HexBytes("concurrent-consume-token")
	userID := internal.HexBytes("concurrent-consume-user")
	processID := internal.HexBytes("concurrent-consume-process")
	address := internal.HexBytes{0x01, 0x02, 0x03}
	c.Assert(testDB.SetCSPAuth(token, userID, testCSPBundleID, ""), qt.IsNil)

	const n = MaxVoteOverwritesPerProcess + 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = handles[i%2].ConsumeCSPProcess(token, processID, address)
		})
	}
	wg.Wait()

	consumed := 0
	for i := range n {
		if errs[i] == nil {
			consumed++
			continue
		}
		c.Assert(errs[i], qt.ErrorIs, ErrProcessAlreadyConsumed)
	}
	c.Assert(consumed, qt.Equals, MaxVoteOverwritesPerProcess+1)

	// once over budget, even another address is refused as consumed
	other := internal.HexBytes{0xaa, 0xbb}
	c.Assert(testDB.ConsumeCSPProcess(token, processID, other), qt.ErrorIs, ErrProcessAlreadyConsumed)
}

// TestConsumeCSPProcessAddressPin pins that a consumption row stays pinned to the
// first address used to vote, also when the second vote arrives via another replica.
func TestConsumeCSPProcessAddressPin(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	replica := secondReplica(c)

	token := internal.HexBytes("address-pin-token")
	userID := internal.HexBytes("address-pin-user")
	processID := internal.HexBytes("address-pin-process")
	c.Assert(testDB.SetCSPAuth(token, userID, testCSPBundleID, ""), qt.IsNil)

	address := internal.HexBytes{0x01}
	c.Assert(testDB.ConsumeCSPProcess(token, processID, address), qt.IsNil)
	// a vote overwrite from the same address is allowed, even via another replica
	c.Assert(replica.ConsumeCSPProcess(token, processID, address), qt.IsNil)
	// a different address is refused
	other := internal.HexBytes{0x02}
	c.Assert(replica.ConsumeCSPProcess(token, processID, other), qt.ErrorIs, ErrInvalidData)
}

// TestConcurrentReserveManagedPublish pins that the managed publish quota cannot be
// exceeded by concurrent reservations across replicas: the quota check is part of the
// increment itself.
func TestConcurrentReserveManagedPublish(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	replica := secondReplica(c)
	handles := []*MongoStorage{testDB, replica}

	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	const limit = 5
	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = handles[i%2].ReserveManagedPublish(testOrgAddress, limit)
		})
	}
	wg.Wait()

	reserved := 0
	for i := range n {
		if errs[i] == nil {
			reserved++
			continue
		}
		c.Assert(errs[i], qt.ErrorIs, ErrManagedQuotaReached)
	}
	c.Assert(reserved, qt.Equals, limit)

	org, err := testDB.Organization(testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.Counters.ManagedProcesses, qt.Equals, limit)

	// a missing organization is reported as such, not as a full quota
	err = testDB.ReserveManagedPublish(testNonExistentOrg, limit)
	c.Assert(err, qt.IsNotNil)
	c.Assert(err, qt.Not(qt.ErrorIs), ErrManagedQuotaReached)
}

// TestConcurrentMigrations pins that several replicas starting at once against the
// same fresh database serialize on the cross-instance migration lock, so every
// migration runs and is recorded exactly once.
func TestConcurrentMigrations(t *testing.T) {
	c := qt.New(t)
	dbName := test.RandomDatabaseName()

	const n = 4
	stores := make([]*MongoStorage, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			stores[i], errs[i] = New(mongoURI, dbName)
		})
	}
	wg.Wait()
	for i := range n {
		c.Assert(errs[i], qt.IsNil)
		c.Cleanup(stores[i].Close)
	}

	migs, err := getAppliedMigrations(context.Background(), stores[0].migrations)
	c.Assert(err, qt.IsNil)
	seen := map[int]bool{}
	for _, m := range migs {
		c.Assert(seen[m.Version], qt.IsFalse, qt.Commentf("migration %d recorded twice", m.Version))
		seen[m.Version] = true
	}
	c.Assert(migs, qt.HasLen, len(migrations.SortedByVersionAsc()))
}
