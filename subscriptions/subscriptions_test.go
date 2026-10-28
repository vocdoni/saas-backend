package subscriptions

import (
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

func newProcessTx(durationSeconds uint32) *models.Tx {
	return &models.Tx{
		Payload: &models.Tx_NewProcess{
			NewProcess: &models.NewProcessTx{
				Txtype: models.TxType_NEW_PROCESS,
				Process: &models.Process{
					EnvelopeType: &models.EnvelopeType{},
					VoteOptions:  &models.ProcessVoteOptions{},
					Duration:     durationSeconds,
				},
			},
		},
	}
}

func TestHasTxPermission(t *testing.T) {
	c := qt.New(t)
	org := &db.Organization{Address: testOrgAddress}
	admin := &db.User{Organizations: []db.OrganizationUser{{Address: testOrgAddress, Role: db.AdminRole}}}
	manager := &db.User{Organizations: []db.OrganizationUser{{Address: testOrgAddress, Role: db.ManagerRole}}}
	subs := &Subscriptions{db: &mockMongoStorage{}}
	const day = 24 * 60 * 60

	// no plan is needed: an admin may publish anything within the fixed restrictions
	ok, err := subs.HasTxPermission(newProcessTx(MaxProcessDurationDays*day), models.TxType_NEW_PROCESS, org, admin)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	_, err = subs.HasTxPermission(newProcessTx(MaxProcessDurationDays*day+1), models.TxType_NEW_PROCESS, org, admin)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)

	_, err = subs.HasTxPermission(newProcessTx(day), models.TxType_NEW_PROCESS, org, manager)
	c.Assert(err, qt.ErrorIs, errors.ErrUserHasNoAdminRole)

	_, err = subs.HasTxPermission(newProcessTx(day), models.TxType_NEW_PROCESS, nil, admin)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidData)
}

func TestHasDBPermission(t *testing.T) {
	c := qt.New(t)
	mockDB := &mockMongoStorage{
		users: map[string]*db.User{
			"test@example.com": {
				Email: "test@example.com",
				Organizations: []db.OrganizationUser{
					{Address: testOrgAddress, Role: db.ViewerRole},
					{Address: testAnotherOrgAddress, Role: db.AdminRole},
				},
			},
		},
	}
	subs := &Subscriptions{db: mockDB}

	_, err := subs.HasDBPermission("notfound@example.com", testOrgAddress, InviteUser)
	c.Assert(err, qt.ErrorMatches, "could not get user.*")
	// not an admin
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, InviteUser)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, DeleteUser)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")
	_, err = subs.HasDBPermission("test@example.com", testOrgAddress, CreateSubOrg)
	c.Assert(err, qt.ErrorMatches, "user does not have admin role")

	hasPermission, err := subs.HasDBPermission("test@example.com", testAnotherOrgAddress, InviteUser)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)
	hasPermission, err = subs.HasDBPermission("test@example.com", testAnotherOrgAddress, CreateSubOrg)
	c.Assert(err, qt.IsNil)
	c.Assert(hasPermission, qt.IsTrue)
}

func TestOrgCanAddWriter(t *testing.T) {
	c := qt.New(t)
	mockDB := &mockMongoStorage{writers: map[string]int64{testOrgAddress.String(): MaxWritersPerOrg - 1}}
	subs := &Subscriptions{db: mockDB}

	// one seat left
	c.Assert(subs.OrgCanAddWriter(testOrgAddress), qt.IsNil)

	mockDB.writers[testOrgAddress.String()] = MaxWritersPerOrg
	c.Assert(subs.OrgCanAddWriter(testOrgAddress), qt.ErrorIs, errors.ErrMaxWritersReached)
}

func TestIsIntegrator(t *testing.T) {
	c := qt.New(t)
	subs := &Subscriptions{}

	c.Assert(subs.IsIntegrator(nil), qt.IsFalse)
	c.Assert(subs.IsIntegrator(&db.Organization{}), qt.IsFalse)
	c.Assert(subs.IsIntegrator(&db.Organization{Integrator: true}), qt.IsTrue)
	// a managed organization is never an integrator, even if flagged
	c.Assert(subs.IsIntegrator(&db.Organization{Integrator: true, ManagedBy: testOrgAddress}), qt.IsFalse)
}

func TestCanCreateManagedOrg(t *testing.T) {
	c := qt.New(t)
	subs := &Subscriptions{}

	c.Assert(subs.CanCreateManagedOrg(&db.Organization{}), qt.ErrorIs, errors.ErrNotAnIntegrator)
	// no cap on how many it manages
	c.Assert(subs.CanCreateManagedOrg(&db.Organization{Integrator: true}), qt.IsNil)
}

func TestOrgCanCreateVotingProcessDraft(t *testing.T) {
	c := qt.New(t)
	integrator := common.Address{0x02}
	managed := common.Address{0x03}
	mockDB := &mockMongoStorage{
		orgs: map[string]*db.Organization{
			testOrgAddress.String(): {Address: testOrgAddress},
			integrator.String():     {Address: integrator, Integrator: true},
			managed.String():        {Address: managed, ManagedBy: integrator},
		},
		votingDraftCounts: map[string]int64{},
	}
	subs := &Subscriptions{db: mockDB}

	mockDB.votingDraftCounts[testOrgAddress.String()] = MaxDraftsPerOrg - 1
	c.Assert(subs.OrgCanCreateVotingProcessDraft(testOrgAddress), qt.IsNil)
	mockDB.votingDraftCounts[testOrgAddress.String()] = MaxDraftsPerOrg
	c.Assert(subs.OrgCanCreateVotingProcessDraft(testOrgAddress), qt.ErrorIs, errors.ErrMaxDraftsReached)

	// an integrator's own organization holds no processes; its managed ones do
	c.Assert(subs.OrgCanCreateVotingProcessDraft(integrator), qt.ErrorIs, errors.ErrIntegratorTopLevelOrgCannotOwnProcess)
	c.Assert(subs.OrgCanCreateVotingProcessDraft(managed), qt.IsNil)
}

func TestOrgCanAddNMembers(t *testing.T) {
	c := qt.New(t)
	mockDB := &mockMongoStorage{orgMembers: map[string]int64{testOrgAddress.String(): MaxMembersPerOrg - 10}}
	subs := &Subscriptions{db: mockDB}

	c.Assert(subs.OrgCanAddNMembers(testOrgAddress, 10), qt.IsNil)
	c.Assert(subs.OrgCanAddNMembers(testOrgAddress, 11), qt.ErrorIs, errors.ErrExceedsOrganizationMembersLimit)
}

func TestOrgCanPublishProcess(t *testing.T) {
	c := qt.New(t)
	subs := &Subscriptions{}
	const day = 24 * 60 * 60

	c.Assert(subs.OrgCanPublishProcess(&db.Organization{}, MaxProcessDurationDays*day), qt.IsNil)
	c.Assert(subs.OrgCanPublishProcess(&db.Organization{}, MaxProcessDurationDays*day+1), qt.ErrorIs, errors.ErrInvalidData)
	c.Assert(subs.OrgCanPublishProcess(&db.Organization{Integrator: true}, day),
		qt.ErrorIs, errors.ErrIntegratorTopLevelOrgCannotOwnProcess)
}

// Mock implementation of the necessary db.MongoStorage methods for testing
type mockMongoStorage struct {
	users map[string]*db.User
	orgs  map[string]*db.Organization
	// keyed by orgAddress string
	writers    map[string]int64
	orgMembers map[string]int64
	// keyed by orgAddress string; draft voting-process count
	votingDraftCounts map[string]int64
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

func (m *mockMongoStorage) CountOrgWriters(addr common.Address) (int64, error) {
	return m.writers[addr.String()], nil
}

func (m *mockMongoStorage) CountOrgMembers(addr common.Address) (int64, error) {
	return m.orgMembers[addr.String()], nil
}

func (m *mockMongoStorage) CountVotingProcesses(addr common.Address, _ db.DraftFilter) (int64, error) {
	return m.votingDraftCounts[addr.String()], nil
}
