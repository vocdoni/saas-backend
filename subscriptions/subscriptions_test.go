package subscriptions

import (
	"testing"

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
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	plans map[uint64]*db.Plan
	users map[string]*db.User
	orgs  map[string]*db.Organization
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
