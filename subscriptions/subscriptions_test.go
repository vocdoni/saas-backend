package subscriptions

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
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
			PlanID: "", // No plan
		},
	}

	// Create a mock organization with a subscription plan
	orgWithPlan := &db.Organization{
		Address: testAnotherOrgAddress,
		Subscription: db.OrganizationSubscription{
			PlanID: "plan-1", // Has a plan
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
		plans: map[string]*db.Plan{
			"plan-1": {
				ID:   "plan-1",
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
	c.Assert(err, qt.ErrorIs, errors.ErrOrganizationHasNoSubscription)

	// Test case 2: Organization with a plan
	hasPermission, err := subs.HasTxPermission(tx, models.TxType_SET_ACCOUNT_INFO_URI, orgWithPlan, user)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	// Test case 3: Nil organization
	_, err = subs.HasTxPermission(tx, models.TxType_SET_ACCOUNT_INFO_URI, nil, user)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)
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
					PlanID: "plan-1",
				},
				Counters: db.OrganizationCounters{
					Users:   5,
					SubOrgs: 2,
				},
			},
		},
		plans: map[string]*db.Plan{
			"plan-1": {
				ID:   "plan-1",
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

func TestIsIntegrator(t *testing.T) {
	c := qt.New(t)
	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{
			"plan-1": {ID: "plan-1", IntegratorLimits: db.IntegratorLimits{MaxManagedOrgs: 5}}, // integrator plan
			"plan-2": {ID: "plan-2", IntegratorLimits: db.IntegratorLimits{MaxManagedOrgs: 0}}, // regular plan
		},
	}
	subs := &Subscriptions{db: mockDB}

	c.Assert(subs.IsIntegrator(nil), qt.IsFalse)

	// no override and no plan
	c.Assert(subs.IsIntegrator(&db.Organization{}), qt.IsFalse)

	// a per-org override (manual/admin path) enables regardless of subscription state
	c.Assert(subs.IsIntegrator(&db.Organization{
		IntegratorLimits: &db.IntegratorLimits{MaxManagedOrgs: 2},
	}), qt.IsTrue)

	// an override of zero managed orgs is not an integrator
	c.Assert(subs.IsIntegrator(&db.Organization{
		IntegratorLimits: &db.IntegratorLimits{MaxManagedOrgs: 0},
	}), qt.IsFalse)

	// self-serve via an active subscription to an integrator plan
	c.Assert(subs.IsIntegrator(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: "plan-1", Active: true},
	}), qt.IsTrue)

	// the same plan with a lapsed (inactive) subscription is not an integrator
	c.Assert(subs.IsIntegrator(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: "plan-1", Active: false},
	}), qt.IsFalse)

	// an active subscription to a non-integrator plan is not an integrator
	c.Assert(subs.IsIntegrator(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: "plan-2", Active: true},
	}), qt.IsFalse)
}

func TestEffectiveIntegratorLimits(t *testing.T) {
	c := qt.New(t)
	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{
			"plan-1": {ID: "plan-1", IntegratorLimits: db.IntegratorLimits{
				MaxManagedOrgs: 5, MaxManagedProcesses: 50, MaxManagedCensusSize: 500,
			}},
		},
	}
	subs := &Subscriptions{db: mockDB}

	// nil org returns ErrNotAnIntegrator and does not panic
	_, err := subs.EffectiveIntegratorLimits(nil)
	c.Assert(err, qt.ErrorIs, errors.ErrNotAnIntegrator)

	// a per-org override takes precedence over the plan limits
	override := &db.IntegratorLimits{MaxManagedOrgs: 2, MaxManagedProcesses: 20, MaxManagedCensusSize: 200}
	limits, err := subs.EffectiveIntegratorLimits(&db.Organization{
		IntegratorLimits: override,
		Subscription:     db.OrganizationSubscription{PlanID: "plan-1"},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(limits, qt.DeepEquals, *override)

	// with no override the plan limits are used
	limits, err = subs.EffectiveIntegratorLimits(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: "plan-1"},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(limits, qt.DeepEquals, mockDB.plans["plan-1"].IntegratorLimits)

	// no override and no plan (PlanID=="") returns a typed ErrPlanNotFound, not a 500
	_, err = subs.EffectiveIntegratorLimits(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: ""},
	})
	c.Assert(err, qt.ErrorIs, errors.ErrPlanNotFound)

	// a plan lookup failure is wrapped
	_, err = subs.EffectiveIntegratorLimits(&db.Organization{
		Subscription: db.OrganizationSubscription{PlanID: "plan-unknown"},
	})
	c.Assert(err, qt.ErrorMatches, "could not get subscription plan.*")
}

func TestCanCreateManagedOrg(t *testing.T) {
	c := qt.New(t)
	subs := &Subscriptions{}
	limits := &db.IntegratorLimits{MaxManagedOrgs: 3}

	// a non-integrator org (no override, no plan) is refused
	err := subs.CanCreateManagedOrg(&db.Organization{})
	c.Assert(err, qt.ErrorIs, errors.ErrNotAnIntegrator)

	// one under the limit is allowed
	err = subs.CanCreateManagedOrg(&db.Organization{
		IntegratorLimits: limits,
		Counters:         db.OrganizationCounters{ManagedOrgs: 2},
	})
	c.Assert(err, qt.IsNil)

	// at the limit is rejected
	err = subs.CanCreateManagedOrg(&db.Organization{
		IntegratorLimits: limits,
		Counters:         db.OrganizationCounters{ManagedOrgs: 3},
	})
	c.Assert(err, qt.ErrorIs, errors.ErrMaxManagedOrgsReached)
}

func TestCanPublishForManagedOrg(t *testing.T) {
	c := qt.New(t)
	subs := &Subscriptions{}
	limits := &db.IntegratorLimits{MaxManagedOrgs: 5, MaxManagedProcesses: 5, MaxManagedCensusSize: 100}
	integrator := func(processes, censusSize int) *db.Organization {
		return &db.Organization{
			IntegratorLimits: limits,
			Counters:         db.OrganizationCounters{ManagedProcesses: processes, ManagedCensusSize: censusSize},
		}
	}

	// a non-integrator org (no override, no plan) is refused
	err := subs.CanPublishForManagedOrg(&db.Organization{}, 10)
	c.Assert(err, qt.ErrorIs, errors.ErrNotAnIntegrator)

	// within quota (census exactly at the limit) is allowed
	c.Assert(subs.CanPublishForManagedOrg(integrator(4, 50), 50), qt.IsNil)

	// process count at the limit is rejected
	c.Assert(subs.CanPublishForManagedOrg(integrator(5, 0), 1), qt.ErrorIs, errors.ErrIntegratorQuotaExceeded)

	// census size that would exceed the limit is rejected
	c.Assert(subs.CanPublishForManagedOrg(integrator(0, 90), 11), qt.ErrorIs, errors.ErrIntegratorQuotaExceeded)
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	plans map[string]*db.Plan
	users map[string]*db.User
	orgs  map[string]*db.Organization
}

func (m *mockMongoStorage) Plan(id string) (*db.Plan, error) {
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

func (*mockMongoStorage) CountOrgMembers(_ common.Address) (int64, error) {
	return 0, nil
}

func (*mockMongoStorage) CountCensusParticipants(string) (int64, error) {
	return 0, nil
}

func (*mockMongoStorage) CountProcesses(_ common.Address, _ db.DraftFilter) (int64, error) {
	return 0, nil
}

func (*mockMongoStorage) OrganizationMemberGroup(string, common.Address) (*db.OrganizationMemberGroup, error) {
	return nil, fmt.Errorf("not implemented in mock")
}
