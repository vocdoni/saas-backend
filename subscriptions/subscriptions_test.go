package subscriptions

import (
	"fmt"
	"testing"
	"time"

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

func TestAnnualUsageEnforcement(t *testing.T) {
	c := qt.New(t)

	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)

	org := &db.Organization{
		Address: testOrgAddress,
		Subscription: db.OrganizationSubscription{
			PlanID:        1,
			BillingPeriod: db.BillingPeriodAnnual,
			StartDate:     start,
			RenewalDate:   end,
		},
		Counters: db.OrganizationCounters{
			Processes:  5,
			SentEmails: 10,
		},
	}

	groupID := "group-1"
	mockDB := &mockMongoStorage{
		plans: map[uint64]*db.Plan{
			1: {
				ID:   1,
				Name: "Test Plan",
				Organization: db.PlanLimits{
					MaxProcesses:  2,
					MaxCensus:     100,
					MaxDuration:   365,
					MaxSentEmails: 12,
					MaxSentSMS:    0,
				},
			},
		},
		orgs: map[string]*db.Organization{
			testOrgAddress.String(): org,
		},
		groups: map[string]*db.OrganizationMemberGroup{
			groupID: {
				MemberIDs: make([]string, 11),
			},
		},
		snapshots: map[string]*db.UsageSnapshot{
			usageSnapshotKey(testOrgAddress, start): {
				OrgAddress:  testOrgAddress,
				PeriodStart: start,
				PeriodEnd:   end,
				Baseline: db.UsageSnapshotBaseline{
					Processes:  4,
					SentEmails: 9,
					SentSMS:    0,
				},
			},
		},
	}

	subs := &Subscriptions{db: mockDB}

	user := &db.User{
		Email: "admin@example.com",
		Organizations: []db.OrganizationUser{
			{Address: testOrgAddress, Role: db.AdminRole},
		},
	}

	tx := &models.Tx{
		Payload: &models.Tx_NewProcess{
			NewProcess: &models.NewProcessTx{
				Txtype: models.TxType_NEW_PROCESS,
				Process: &models.Process{
					MaxCensusSize: uint64(20),
					Duration:      1,
					EnvelopeType: &models.EnvelopeType{
						Anonymous:      false,
						CostFromWeight: false,
					},
					VoteOptions: &models.ProcessVoteOptions{
						MaxVoteOverwrites: 0,
					},
				},
			},
		},
	}

	hasPermission, err := subs.HasTxPermission(tx, models.TxType_NEW_PROCESS, org, user)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)

	census := &db.Census{
		OrgAddress:  testOrgAddress,
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
	}
	c.Assert(subs.OrgCanPublishGroupCensus(census, groupID), qt.IsNil)
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	plans     map[uint64]*db.Plan
	users     map[string]*db.User
	orgs      map[string]*db.Organization
	groups    map[string]*db.OrganizationMemberGroup
	snapshots map[string]*db.UsageSnapshot
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

func (m *mockMongoStorage) OrganizationMemberGroup(groupID string, _ common.Address) (*db.OrganizationMemberGroup, error) {
	group, ok := m.groups[groupID]
	if !ok {
		return nil, db.ErrNotFound
	}
	return group, nil
}

func (m *mockMongoStorage) GetUsageSnapshot(orgAddress common.Address, periodStart time.Time) (*db.UsageSnapshot, error) {
	key := usageSnapshotKey(orgAddress, periodStart)
	snapshot, ok := m.snapshots[key]
	if !ok {
		return nil, db.ErrNotFound
	}
	return snapshot, nil
}

func (m *mockMongoStorage) UpsertUsageSnapshot(snapshot *db.UsageSnapshot) error {
	if snapshot == nil {
		return db.ErrInvalidData
	}
	key := usageSnapshotKey(snapshot.OrgAddress, snapshot.PeriodStart)
	if _, ok := m.snapshots[key]; !ok {
		m.snapshots[key] = snapshot
	}
	return nil
}

func usageSnapshotKey(orgAddress common.Address, periodStart time.Time) string {
	return fmt.Sprintf("%s:%s", orgAddress.String(), periodStart.UTC().Format(time.RFC3339Nano))
}
