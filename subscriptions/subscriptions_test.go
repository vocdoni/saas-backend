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

const (
	integratorPlanID = "integrator-plan"
	tinyPlanID       = "tiny-plan"
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
			"plan-1": {ID: "plan-1", IntegratorLimits: db.IntegratorLimits{MaxManagedOrgs: 5}},
		},
	}
	subs := &Subscriptions{db: mockDB}

	// nil org returns ErrNotAnIntegrator and does not panic
	_, err := subs.EffectiveIntegratorLimits(nil)
	c.Assert(err, qt.ErrorIs, errors.ErrNotAnIntegrator)

	// a per-org override takes precedence over the plan limits
	override := &db.IntegratorLimits{MaxManagedOrgs: 2}
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
	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{
			// the aggregate process cap comes from the plan's top-level Organization limit
			integratorPlanID: {
				ID:               integratorPlanID,
				Organization:     db.PlanLimits{MaxProcesses: 5},
				IntegratorLimits: db.IntegratorLimits{MaxManagedOrgs: 5},
			},
		},
	}
	subs := &Subscriptions{db: mockDB}
	integrator := func(processes int) *db.Organization {
		return &db.Organization{
			Subscription: db.OrganizationSubscription{PlanID: integratorPlanID, Active: true},
			Counters:     db.OrganizationCounters{ManagedProcesses: processes},
		}
	}

	// a non-integrator org (no override, no plan) is refused
	err := subs.CanPublishForManagedOrg(&db.Organization{})
	c.Assert(err, qt.ErrorIs, errors.ErrNotAnIntegrator)

	// within the process quota is allowed
	c.Assert(subs.CanPublishForManagedOrg(integrator(4)), qt.IsNil)

	// process count at the limit is rejected
	c.Assert(subs.CanPublishForManagedOrg(integrator(5)), qt.ErrorIs, errors.ErrIntegratorQuotaExceeded)
}

// TestManagedOrgLimitsUseIntegratorPlan asserts that a managed org's limits are governed
// by its integrator's plan and aggregate quotas, never by its own (throwaway default) plan.
// In every case the managed org's own plan is intentionally near-zero while the integrator's
// plan is generous: if the managed org were bound by its own plan it would be rejected.
func TestManagedOrgLimitsUseIntegratorPlan(t *testing.T) {
	c := qt.New(t)

	integratorAddr := common.Address{0xAA}
	managedAddr := common.Address{0xBB}
	standaloneAddr := common.Address{0xCC} // not managed, same tiny plan — the control

	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{
			// generous integrator plan
			integratorPlanID: {
				ID: integratorPlanID,
				Organization: db.PlanLimits{
					MaxProcesses: 100, MaxCensus: 1000, MaxDuration: 30, MaxDrafts: 10, MaxVotes: 100,
				},
				Features: db.Features{Anonymous: true, TwoFaEmail: 100, TwoFaSms: 100},
			},
			// near-zero throwaway plan the managed/standalone orgs are seeded with
			tinyPlanID: {
				ID: tinyPlanID,
				Organization: db.PlanLimits{
					MaxProcesses: 0, MaxCensus: 1, MaxDuration: 0, MaxDrafts: 0, MaxVotes: 1,
				},
				Features: db.Features{Anonymous: false, TwoFaEmail: 0, TwoFaSms: 0},
			},
		},
		orgs: map[string]*db.Organization{
			integratorAddr.String(): {
				Address:      integratorAddr,
				Subscription: db.OrganizationSubscription{PlanID: integratorPlanID, Active: true},
			},
			managedAddr.String(): {
				Address:      managedAddr,
				ManagedBy:    integratorAddr,
				Subscription: db.OrganizationSubscription{PlanID: tinyPlanID, Active: true},
			},
			standaloneAddr.String(): {
				Address:      standaloneAddr,
				Subscription: db.OrganizationSubscription{PlanID: tinyPlanID, Active: true},
			},
		},
		// shared-pool consumption across the integrator's managed orgs
		membersManagedBy:    map[string]int64{integratorAddr.String(): 5},
		sentEmailsManagedBy: map[string]int{integratorAddr.String(): 10},
		sentSMSManagedBy:    map[string]int{integratorAddr.String(): 10},
		sentVotesManagedBy:  map[string]int{integratorAddr.String(): 10},
		orgMembers:          map[string]int64{standaloneAddr.String(): 5},
		groups: map[string]*db.OrganizationMemberGroup{
			"group-1": {MemberIDs: []string{"a", "b", "c"}},
		},
	}
	subs := &Subscriptions{db: mockDB}

	adminUser := &db.User{
		Email: "admin@example.com",
		Organizations: []db.OrganizationUser{
			{Address: managedAddr, Role: db.AdminRole},
			{Address: standaloneAddr, Role: db.AdminRole},
		},
	}

	// a NEW_PROCESS tx requesting an anonymous election bigger than a test-sized one.
	newProcessTx := func() *models.Tx {
		return &models.Tx{
			Payload: &models.Tx_NewProcess{
				NewProcess: &models.NewProcessTx{
					Txtype: models.TxType_NEW_PROCESS,
					Process: &models.Process{
						MaxCensusSize: uint64(db.TestMaxCensusSize) + 50,
						Duration:      3600,
						EnvelopeType:  &models.EnvelopeType{Anonymous: true},
						VoteOptions:   &models.ProcessVoteOptions{},
					},
				},
			},
		}
	}

	managedOrg := mockDB.orgs[managedAddr.String()]
	standaloneOrg := mockDB.orgs[standaloneAddr.String()]

	// --- Per-process census cap + capability flags (HasTxPermission) ---
	// Managed org: the census fits the integrator's generous plan (MaxCensus 1000), anonymous is
	// allowed by that plan, and only the per-org *process-count* cap is skipped (the integrator
	// aggregate governs that at publish).
	ok, err := subs.HasTxPermission(newProcessTx(), models.TxType_NEW_PROCESS, managedOrg, adminUser)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	// Standalone org on the tiny plan: rejected — its MaxCensus (1) is exceeded.
	_, err = subs.HasTxPermission(newProcessTx(), models.TxType_NEW_PROCESS, standaloneOrg, adminUser)
	c.Assert(err, qt.Not(qt.IsNil))

	// A SET_PROCESS_CENSUS tx carries a SetProcess payload; the census update is bounded by the
	// governing plan's MaxCensus and must be read with GetSetProcess (reading it as a NewProcess
	// would nil-panic).
	setProcessCensusTx := func(censusSize uint64) *models.Tx {
		return &models.Tx{
			Payload: &models.Tx_SetProcess{
				SetProcess: &models.SetProcessTx{
					Txtype:     models.TxType_SET_PROCESS_CENSUS,
					ProcessId:  []byte{0x01},
					CensusSize: &censusSize,
				},
			},
		}
	}
	// Managed org: a census update within the integrator plan's MaxCensus (1000) is allowed.
	ok, err = subs.HasTxPermission(setProcessCensusTx(500), models.TxType_SET_PROCESS_CENSUS, managedOrg, adminUser)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	// Managed org: a census update beyond the integrator plan's MaxCensus is rejected.
	_, err = subs.HasTxPermission(setProcessCensusTx(2000), models.TxType_SET_PROCESS_CENSUS, managedOrg, adminUser)
	c.Assert(err, qt.ErrorIs, errors.ErrProcessCensusSizeExceedsPlanLimit)

	// --- MaxDrafts value cap (OrgHasPermission) ---
	// Managed org: governed by integrator MaxDrafts (10) — allowed; standalone tiny plan
	// (MaxDrafts 0) — rejected.
	c.Assert(subs.OrgHasPermission(managedAddr, CreateDraft), qt.IsNil)
	c.Assert(subs.OrgHasPermission(standaloneAddr, CreateDraft), qt.ErrorIs, errors.ErrMaxDraftsReached)

	// --- Members shared pool (OrgCanAddNMembers) ---
	// Managed org: integrator MaxCensus (1000) vs shared-pool count (5) — allowed.
	c.Assert(subs.OrgCanAddNMembers(managedAddr, 3), qt.IsNil)
	// Standalone tiny plan (MaxCensus 1) vs its own members (5) — rejected.
	c.Assert(subs.OrgCanAddNMembers(standaloneAddr, 3), qt.ErrorIs, errors.ErrExceedsOrganizationMembersLimit)

	// --- Census participants: integrator-governed cap for managed, own plan for standalone ---
	// Managed org: integrator MaxCensus (1000) — 500 allowed, 2000 rejected (its own tiny
	// plan would have rejected 500, proving the integrator's plan governs).
	c.Assert(subs.OrgCanAddCensusParticipants(managedAddr, "census-x", 500), qt.IsNil)
	c.Assert(subs.OrgCanAddCensusParticipants(managedAddr, "census-x", 2000),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsPlanLimit)
	// Standalone tiny plan (MaxCensus 1): even small adds are rejected.
	c.Assert(subs.OrgCanAddCensusParticipants(standaloneAddr, "census-x", 10),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsPlanLimit)

	// --- 2FA shared pool (OrgCanPublishGroupCensus) ---
	emailCensus := func(orgAddr common.Address) *db.Census {
		return &db.Census{OrgAddress: orgAddr, TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}}
	}
	// Managed org: integrator TwoFaEmail (100) - shared sent (10) = 90 remaining >= 3 members — allowed.
	c.Assert(subs.OrgCanPublishGroupCensus(emailCensus(managedAddr), "group-1"), qt.IsNil)
	// Standalone tiny plan: TwoFaEmail 0 - 0 = 0 remaining < 3 members — rejected.
	c.Assert(subs.OrgCanPublishGroupCensus(emailCensus(standaloneAddr), "group-1"),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsEmailAllowance)

	// --- Vote shared pool (OrgCanPublishGroupCensus) ---
	// A census with no 2FA fields isolates the vote allowance from the email/sms checks.
	plainCensus := func(orgAddr common.Address) *db.Census {
		return &db.Census{OrgAddress: orgAddr}
	}
	// Managed org: integrator MaxVotes (100) - shared sent (10) = 90 remaining >= 3 members — allowed.
	c.Assert(subs.OrgCanPublishGroupCensus(plainCensus(managedAddr), "group-1"), qt.IsNil)
	// Standalone tiny plan: MaxVotes 1 - own sent (0) = 1 remaining < 3 members — rejected.
	c.Assert(subs.OrgCanPublishGroupCensus(plainCensus(standaloneAddr), "group-1"),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsVoteAllowance)
}

// TestManagedOrgSharedPoolExceeded asserts the integrator's shared pool is enforced: when
// combined consumption across the integrator's managed orgs reaches the integrator limit,
// a managed org is rejected even though its own (throwaway) plan is irrelevant.
func TestManagedOrgSharedPoolExceeded(t *testing.T) {
	c := qt.New(t)

	integratorAddr := common.Address{0xA1}
	managedAddr := common.Address{0xB1}

	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{
			integratorPlanID: {
				ID:           integratorPlanID,
				Organization: db.PlanLimits{MaxCensus: 100, MaxVotes: 50},
				Features:     db.Features{TwoFaSms: 50},
			},
			tinyPlanID: {ID: tinyPlanID, Organization: db.PlanLimits{MaxCensus: 1}},
		},
		orgs: map[string]*db.Organization{
			integratorAddr.String(): {
				Address:      integratorAddr,
				Subscription: db.OrganizationSubscription{PlanID: integratorPlanID, Active: true},
			},
			managedAddr.String(): {
				Address:      managedAddr,
				ManagedBy:    integratorAddr,
				Subscription: db.OrganizationSubscription{PlanID: tinyPlanID, Active: true},
			},
		},
		// pool already near the integrator's limits
		membersManagedBy:   map[string]int64{integratorAddr.String(): 99},
		sentSMSManagedBy:   map[string]int{integratorAddr.String(): 50},
		sentVotesManagedBy: map[string]int{integratorAddr.String(): 50},
		groups: map[string]*db.OrganizationMemberGroup{
			"group-1": {MemberIDs: []string{"a"}},
		},
	}
	subs := &Subscriptions{db: mockDB}

	// members: pool at 99, integrator MaxCensus 100 → adding 2 exceeds.
	c.Assert(subs.OrgCanAddNMembers(managedAddr, 2), qt.ErrorIs, errors.ErrExceedsOrganizationMembersLimit)
	// 2FA SMS: pool at 50, integrator TwoFaSms 50 → 0 remaining, 1 member needed → rejected.
	smsCensus := &db.Census{OrgAddress: managedAddr, TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldPhone}}
	c.Assert(subs.OrgCanPublishGroupCensus(smsCensus, "group-1"),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsSMSAllowance)
	// votes: pool at 50, integrator MaxVotes 50 → 0 remaining, 1 member needed → rejected.
	voteCensus := &db.Census{OrgAddress: managedAddr}
	c.Assert(subs.OrgCanPublishGroupCensus(voteCensus, "group-1"),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsVoteAllowance)
}

// TestManagedOrgMissingIntegratorFailsClosed asserts a managed org whose integrator cannot
// be resolved is rejected rather than silently falling back to its own plan.
func TestManagedOrgMissingIntegratorFailsClosed(t *testing.T) {
	c := qt.New(t)

	managedAddr := common.Address{0xD1}
	mockDB := &mockMongoStorage{
		plans: map[string]*db.Plan{tinyPlanID: {ID: tinyPlanID}},
		orgs: map[string]*db.Organization{
			managedAddr.String(): {
				Address:      managedAddr,
				ManagedBy:    common.Address{0xDE, 0xAD}, // integrator absent from the mock
				Subscription: db.OrganizationSubscription{PlanID: tinyPlanID, Active: true},
			},
		},
	}
	subs := &Subscriptions{db: mockDB}

	c.Assert(subs.OrgCanAddNMembers(managedAddr, 1), qt.ErrorIs, errors.ErrOrganizationNotFound)
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	plans map[string]*db.Plan
	users map[string]*db.User
	orgs  map[string]*db.Organization
	// keyed by integrator address string; default 0 when absent
	membersManagedBy    map[string]int64
	sentEmailsManagedBy map[string]int
	sentSMSManagedBy    map[string]int
	sentVotesManagedBy  map[string]int
	// keyed by orgAddress string
	orgMembers map[string]int64
	// keyed by groupID
	groups map[string]*db.OrganizationMemberGroup
	// keyed by orgAddress string; draft process count
	draftCounts map[string]int64
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

func (m *mockMongoStorage) CountOrgMembers(addr common.Address) (int64, error) {
	return m.orgMembers[addr.String()], nil
}

func (m *mockMongoStorage) CountMembersManagedBy(integratorAddr common.Address) (int64, error) {
	return m.membersManagedBy[integratorAddr.String()], nil
}

func (m *mockMongoStorage) SumSentEmailsManagedBy(integratorAddr common.Address) (int, error) {
	return m.sentEmailsManagedBy[integratorAddr.String()], nil
}

func (m *mockMongoStorage) SumSentSMSManagedBy(integratorAddr common.Address) (int, error) {
	return m.sentSMSManagedBy[integratorAddr.String()], nil
}

func (m *mockMongoStorage) SumSentVotesManagedBy(integratorAddr common.Address) (int, error) {
	return m.sentVotesManagedBy[integratorAddr.String()], nil
}

func (*mockMongoStorage) CountCensusParticipants(string) (int64, error) {
	return 0, nil
}

func (m *mockMongoStorage) CountProcesses(addr common.Address, _ db.DraftFilter) (int64, error) {
	return m.draftCounts[addr.String()], nil
}

func (m *mockMongoStorage) OrganizationMemberGroup(groupID string, _ common.Address) (*db.OrganizationMemberGroup, error) {
	group, ok := m.groups[groupID]
	if !ok {
		return nil, fmt.Errorf("group not found in mock")
	}
	return group, nil
}
