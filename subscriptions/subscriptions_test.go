package subscriptions

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/proto/build/go/models"
)

// Common test constants
var (
	testOrgAddress        = common.Address{0x01, 0x23, 0x45, 0x67, 0x89}
	testAnotherOrgAddress = common.Address{0x10, 0x11, 0x12, 0x13, 0x14}
)

func TestHasTxPermission(t *testing.T) {
	c := qt.New(t)
	// Create a mock organization without a subscription plan
	orgWithoutPlan := &db.Organization{
		Address: testOrgAddress,
		Subscription: db.OrganizationSubscription{
			PlanID: 0, // No plan
		},
	}

	// Create a mock organization with a subscription plan
	orgWithPlan := &db.Organization{
		Address: testAnotherOrgAddress,
		Subscription: db.OrganizationSubscription{
			PlanID: 1, // Has a plan
		},
	}

	// Create a mock user
	user := &db.User{
		Email: "test@example.com",
		Organizations: []db.OrganizationUser{
			{
				Address: testOrgAddress,
				Role:    db.AdminRole,
			},
			{
				Address: testAnotherOrgAddress,
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
						Address: testOrgAddress,
						Role:    db.ViewerRole,
					},
					{
						Address: testAnotherOrgAddress,
						Role:    db.AdminRole,
					},
				},
			},
		},
		orgs: map[string]*db.Organization{
			testOrgAddress.String(): {
				Address: testOrgAddress,
			},
			testAnotherOrgAddress.String(): {
				Address: testAnotherOrgAddress,
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
					MaxTeamMembers: 10,
					MaxSubOrgs:     5,
				},
			},
		},
	}

	// Create a subscriptions service with the mock DB
	subs := &Subscriptions{
		db: mockDB,
	}

	// Test case: Non-existent user
	_, err := subs.HasDBPermission("notfound@example.com", testOrgAddress, InviteUser)
	c.Assert(err, qt.ErrorMatches, "could not get user.*")
	// Test case: Not an admin
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, InviteUser)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, DeleteUser)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, CreateSubOrg)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")

	// Test case 2: Organization with a plan - invite user
	hasPermission, err := subs.HasDBPermission("test@example.com", testAnotherOrgAddress, InviteUser)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	// Test case 3: Organization with a plan - create sub org
	hasPermission, err = subs.HasDBPermission("test@example.com", testAnotherOrgAddress, CreateSubOrg)
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

func (m *mockMongoStorage) Organization(address common.Address) (org *db.Organization, err error) {
	org, ok := m.orgs[address.String()]
	if !ok {
		return nil, db.ErrNotFound
	}
	return org, nil
}

func (m *mockMongoStorage) OrganizationWithParent(address common.Address) (
	org *db.Organization, parent *db.Organization, err error,
) {
	org, ok := m.orgs[address.String()]
	if !ok {
		return nil, nil, db.ErrNotFound
	}
	return org, nil, nil
}

func (*mockMongoStorage) CountProcesses(_ common.Address, _ db.DraftFilter) (int64, error) {
	return 0, nil
}
