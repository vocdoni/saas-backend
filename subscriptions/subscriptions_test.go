package subscriptions

import (
	"fmt"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/proto/build/go/models"
)

func TestHasTxPermission(t *testing.T) {
	c := qt.New(t)
	// Create a mock organization without a subscription plan
	orgWithoutPlan := &db.Organization{
		Address: "0x123",
		Subscription: db.OrganizationSubscription{
			PlanID: 0, // No plan
		},
	}

	// Create a mock organization with a subscription plan
	orgWithPlan := &db.Organization{
		Address: "0x456",
		Subscription: db.OrganizationSubscription{
			PlanID: 1, // Has a plan
		},
	}

	// Create a mock user
	user := &db.User{
		Email: "test@example.com",
		Organizations: []db.OrganizationUser{
			{
				Address: "0x123",
				Role:    db.AdminRole,
			},
			{
				Address: "0x456",
				Role:    db.AdminRole,
			},
		},
	}

	// Create a mock transaction
	tx := &models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Txtype: models.TxType_SET_ACCOUNT_INFO_URI,
			},
		},
	}

	// Create a mock DB that returns a plan for ID 1
	mockDB := &mockMongoStorage{
		plans: map[uint64]*db.Plan{
			1: {
				ID:   1,
				Name: "Test Plan",
				Organization: db.PlanLimits{
					MaxProcesses: 10,
				},
			},
		},
	}

	// Create a subscriptions service with the mock DB
	subs := &Subscriptions{
		db: mockDB,
	}

	// Test case 1: Organization without a plan
	_, err := subs.HasTxPermission(tx, models.TxType_SET_ACCOUNT_INFO_URI, orgWithoutPlan, user)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "organization has no subscription plan")

	// Test case 2: Organization with a plan
	hasPermission, err := subs.HasTxPermission(tx, models.TxType_SET_ACCOUNT_INFO_URI, orgWithPlan, user)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	// Test case 3: Nil organization
	_, err = subs.HasTxPermission(tx, models.TxType_SET_ACCOUNT_INFO_URI, nil, user)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "organization is nil")
}

func TestHasDBPermission(t *testing.T) {
	c := qt.New(t)
	// Create a mock DB that returns specific users and organizations
	mockDB := &mockMongoStorage{
		users: map[string]*db.User{
			"test@example.com": {
				Email: "test@example.com",
				Organizations: []db.OrganizationUser{
					{
						Address: "0x123",
						Role:    db.AdminRole,
					},
					{
						Address: "0x456",
						Role:    db.AdminRole,
					},
					{
						Address: "0x789",
						Role:    db.AdminRole,
					},
				},
			},
		},
		orgs: map[string]*db.Organization{
			"0x123": {
				Address: "0x123",
				Subscription: db.OrganizationSubscription{
					PlanID: 0,
				},
			},
			"0x456": {
				Address: "0x456",
				Subscription: db.OrganizationSubscription{
					PlanID: 1,
				},
				Counters: db.OrganizationCounters{
					Users:   5,
					SubOrgs: 2,
				},
			},
			"0x789": {
				Address: "0x789",
				Subscription: db.OrganizationSubscription{
					PlanID: 1,
				},
				Counters: db.OrganizationCounters{
					Users:   10, // Max users reached
					SubOrgs: 2,
				},
			},
		},
		plans: map[uint64]*db.Plan{
			1: {
				ID:   1,
				Name: "Test Plan",
				Organization: db.PlanLimits{
					Users:   10,
					SubOrgs: 5,
				},
			},
		},
	}

	// Create a subscriptions service with the mock DB
	subs := &Subscriptions{
		db: mockDB,
	}

	// Test case 1: Organization without a plan
	_, err := subs.HasDBPermission("test@example.com", "0x123", InviteUser)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "organization has no subscription plan")

	// Test case 2: Organization with a plan - invite user
	hasPermission, err := subs.HasDBPermission("test@example.com", "0x456", InviteUser)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	// Test case 3: Organization with a plan - create sub org
	hasPermission, err = subs.HasDBPermission("test@example.com", "0x456", CreateSubOrg)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	// Test case 4: Organization with max users reached
	hasPermission, err = subs.HasDBPermission("test@example.com", "0x789", InviteUser)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "max users reached")
	c.Assert(hasPermission, qt.IsFalse)
}

// TestRaceConditionInviteUsers tests the race condition in the HasDBPermission method
// that can allow more users to be added than the plan allows
func TestRaceConditionInviteUsers(t *testing.T) {
	c := qt.New(t)

	// Create a mock DB with an organization that has 9 users (just below the limit of 10)
	mockDB := &mockMongoStorage{
		users: map[string]*db.User{
			"admin@example.com": {
				Email: "admin@example.com",
				Organizations: []db.OrganizationUser{
					{
						Address: "0x999",
						Role:    db.AdminRole,
					},
				},
			},
		},
		orgs: map[string]*db.Organization{
			"0x999": {
				Address: "0x999",
				Subscription: db.OrganizationSubscription{
					PlanID: 1,
				},
				Counters: db.OrganizationCounters{
					Users: 9, // Just below the limit
				},
			},
		},
		plans: map[uint64]*db.Plan{
			1: {
				ID:   1,
				Name: "Test Plan",
				Organization: db.PlanLimits{
					Users: 10, // Max 10 users
				},
			},
		},
	}

	// Create a subscriptions service with the mock DB
	subs := &Subscriptions{
		db: mockDB,
	}

	// Create a function that simulates the inviteOrganizationUserHandler
	// It checks permission and then increments the counter if permission is granted
	// We add a delay between the check and the increment to simulate the race condition
	inviteUser := func(email string, delay time.Duration) bool {
		// Check permission
		hasPermission, err := subs.HasDBPermission("admin@example.com", "0x999", InviteUser)
		if !hasPermission || err != nil {
			return false
		}

		// Add a delay to simulate concurrent requests
		time.Sleep(delay)

		// If permission granted, increment the counter
		err = mockDB.IncrementOrganizationUsersCounter("0x999")
		return err == nil
	}

	// Use a WaitGroup to ensure both goroutines complete
	var wg sync.WaitGroup
	wg.Add(2)

	// Track the results of the invites
	var invite1Success, invite2Success bool

	// Send invites concurrently to trigger the race condition
	// The first invite will check permission, then wait 100ms before incrementing
	// The second invite will check permission during this delay, then increment
	go func() {
		defer wg.Done()
		invite1Success = inviteUser("user1@example.com", 100*time.Millisecond)
	}()

	go func() {
		defer wg.Done()
		// Add a small delay to ensure the first goroutine checks permission first
		time.Sleep(10 * time.Millisecond)
		invite2Success = inviteUser("user2@example.com", 0)
	}()

	// Wait for both invites to complete
	wg.Wait()

	// Both invites should succeed due to the race condition
	c.Assert(invite1Success, qt.IsTrue, qt.Commentf("First invite should succeed"))
	c.Assert(invite2Success, qt.IsTrue, qt.Commentf("Second invite should also succeed due to the race condition"))

	// Check the final user count - it should be 11, which exceeds the limit of 10
	org, err := mockDB.Organization("0x999")
	c.Assert(err, qt.IsNil)
	c.Assert(org.Counters.Users, qt.Equals, 11, qt.Commentf("User count should be 11, exceeding the limit of 10"))

	// This demonstrates the race condition: both invites checked the counter when it was 9,
	// both were granted permission because 9 < 10, and both incremented the counter,
	// resulting in 11 users, which exceeds the limit of 10.
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	plans map[uint64]*db.Plan
	users map[string]*db.User
	orgs  map[string]*db.Organization
	// For race condition testing
	mutex sync.Mutex
}

func (m *mockMongoStorage) Plan(id uint64) (*db.Plan, error) {
	plan, ok := m.plans[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return plan, nil
}

func (m *mockMongoStorage) UserByEmail(email string) (*db.User, error) {
	user, ok := m.users[email]
	if !ok {
		return nil, db.ErrNotFound
	}
	return user, nil
}

func (m *mockMongoStorage) Organization(address string) (org *db.Organization, err error) {
	org, ok := m.orgs[address]
	if !ok {
		return nil, db.ErrNotFound
	}
	return org, nil
}

func (m *mockMongoStorage) OrganizationWithParent(address string) (org *db.Organization, parent *db.Organization, err error) {
	org, ok := m.orgs[address]
	if !ok {
		return nil, nil, db.ErrNotFound
	}
	return org, nil, nil
}

// IncrementOrganizationUsersCounter increments the users counter for the organization
func (m *mockMongoStorage) IncrementOrganizationUsersCounter(address string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	org, ok := m.orgs[address]
	if !ok {
		return fmt.Errorf("organization not found")
	}

	org.Counters.Users++
	return nil
}

// DecrementOrganizationUsersCounter decrements the users counter for the organization
func (m *mockMongoStorage) DecrementOrganizationUsersCounter(address string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	org, ok := m.orgs[address]
	if !ok {
		return fmt.Errorf("organization not found")
	}

	org.Counters.Users--
	return nil
}
