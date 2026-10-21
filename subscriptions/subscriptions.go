// Package subscriptions enforces what an organization may do: its users' roles and the fixed
// pay-per-process restrictions, which are the same for every organization. Spending is not
// limited here: each process pays for itself (pricing, checkout, integrator wallet).
package subscriptions

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/proto/build/go/models"
)

// Config holds the configuration for the subscriptions service.
// It includes a reference to the MongoDB storage used by the service.
type Config struct {
	DB *db.MongoStorage
}

// DBPermission represents the permissions that an organization can have based on its subscription.
type DBPermission int

const (
	// InviteUser represents the permission to invite new users to an organization.
	InviteUser DBPermission = iota
	// DeleteUser represents the permission to remove users from an organization.
	DeleteUser
	// CreateSubOrg represents the permission to create sub-organizations.
	CreateSubOrg
	// CreateOrg represents the permission to create new organizations.
	CreateOrg
)

// The pay-per-process restrictions. They apply to every organization alike, a managed one
// included: each counts on its own, nothing is pooled with its integrator.
const (
	// MaxOrgsPerUser is how many organizations a user may belong to.
	MaxOrgsPerUser = 15
	// MaxWritersPerOrg is how many users may hold a write role (admin or manager) in an
	// organization, pending invitations to one included.
	MaxWritersPerOrg = 5
	// MaxDraftsPerOrg is how many voting process drafts an organization may hold.
	MaxDraftsPerOrg = 20
	// MaxProcessDurationDays is the longest a voting process may run.
	MaxProcessDurationDays = 90
)

// MaxMembersPerOrg is the size limit of an organization's memberbase. A var for tests.
var MaxMembersPerOrg int64 = 50_000

// DBInterface defines the database methods required by the Subscriptions service
type DBInterface interface {
	UserByEmail(email string) (*db.User, error)
	Organization(address common.Address) (*db.Organization, error)
	OrganizationUsers(address common.Address) ([]db.User, error)
	PendingInvitations(organizationAddress common.Address) ([]db.OrganizationInvite, error)
	CountOrgMembers(orgAddress common.Address) (int64, error)
	CountVotingProcesses(orgAddress common.Address, draft db.DraftFilter) (int64, error)
}

// Subscriptions is the service that enforces organization permissions and restrictions.
type Subscriptions struct {
	db DBInterface
}

// New creates a new Subscriptions service with the given configuration.
func New(conf *Config) *Subscriptions {
	if conf == nil {
		return nil
	}
	return &Subscriptions{
		db: conf.DB,
	}
}

// managed reports whether org is a managed organization (created and owned by an integrator).
func managed(org *db.Organization) bool {
	return org != nil && org.ManagedBy != (common.Address{})
}

// durationAllowed refuses a process running longer than MaxProcessDurationDays.
func durationAllowed(durationSeconds uint32) error {
	if time.Duration(durationSeconds)*time.Second > MaxProcessDurationDays*24*time.Hour {
		return errors.ErrInvalidData.Withf("process duration exceeds the limit of %d days", MaxProcessDurationDays)
	}
	return nil
}

// HasTxPermission checks if the organization has permission to perform the given transaction.
func (*Subscriptions) HasTxPermission(
	tx *models.Tx,
	txType models.TxType,
	org *db.Organization,
	user *db.User,
) (bool, error) {
	if org == nil {
		return false, errors.ErrInvalidData.With("organization is nil")
	}
	// NEW_PROCESS, built by the /processes publish, is the only tx the service checks.
	if txType != models.TxType_NEW_PROCESS {
		return false, fmt.Errorf("unsupported txtype")
	}
	// check if the user has the admin role for the organization
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		return false, errors.ErrUserHasNoAdminRole
	}
	newProcess := tx.GetNewProcess()
	if newProcess == nil || newProcess.Process == nil {
		return false, errors.ErrInvalidData.With("missing new-process payload")
	}
	if err := durationAllowed(newProcess.Process.Duration); err != nil {
		return false, err
	}
	return true, nil
}

// HasDBPermission checks if the user has permission to perform the given action in the organization stored in the DB
func (p *Subscriptions) HasDBPermission(userEmail string, orgAddress common.Address, permission DBPermission) (bool, error) {
	user, err := p.db.UserByEmail(userEmail)
	if err != nil {
		return false, fmt.Errorf("could not get user: %v", err)
	}
	switch permission {
	case InviteUser, DeleteUser, CreateSubOrg:
		if !user.HasRoleFor(orgAddress, db.AdminRole) {
			return false, errors.ErrUserHasNoAdminRole
		}
		return true, nil
	case CreateOrg:
		// Check if the user can create more organizations based on the MaxOrgsPerUser limit
		if len(user.Organizations) >= MaxOrgsPerUser {
			return false, errors.ErrMaxOrganizationsReached.Withf(
				"user is part of %d organizations, max allowed is %d",
				len(user.Organizations),
				MaxOrgsPerUser,
			)
		}
		return true, nil
	default:
		return false, fmt.Errorf("permission not found")
	}
}

// writeRole reports whether role grants write access to an organization.
func writeRole(role db.UserRole) bool {
	return role == db.AdminRole || role == db.ManagerRole
}

// OrgCanAddWriter checks that one more user may hold a write role (admin or manager) in the
// organization: the users holding one plus the pending invitations to one stay within
// MaxWritersPerOrg. Callers check it only when the role they grant is a write role.
func (p *Subscriptions) OrgCanAddWriter(orgAddress common.Address) error {
	users, err := p.db.OrganizationUsers(orgAddress)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	invitations, err := p.db.PendingInvitations(orgAddress)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	writers := 0
	for i := range users {
		if role, ok := users[i].RoleFor(orgAddress); ok && writeRole(role) {
			writers++
		}
	}
	now := time.Now()
	for i := range invitations {
		// an expired invitation can no longer be accepted, so it holds no seat
		if writeRole(invitations[i].Role) && invitations[i].Expiration.After(now) {
			writers++
		}
	}
	if writers >= MaxWritersPerOrg {
		return errors.ErrMaxWritersReached.Withf("(%d)", MaxWritersPerOrg)
	}
	return nil
}

// ownsProcesses refuses an integrator's own organization: it publishes for others through
// managed organizations (one of them may be its own), so it holds no processes itself.
func (p *Subscriptions) ownsProcesses(org *db.Organization) error {
	if p.IsIntegrator(org) {
		return errors.ErrIntegratorTopLevelOrgCannotOwnProcess
	}
	return nil
}

// OrgCanCreateVotingProcessDraft checks that the organization may hold processes and is under
// MaxDraftsPerOrg.
func (p *Subscriptions) OrgCanCreateVotingProcessDraft(orgAddress common.Address) error {
	org, err := p.db.Organization(orgAddress)
	if err != nil {
		return errors.ErrOrganizationNotFound.WithErr(err)
	}
	if err := p.ownsProcesses(org); err != nil {
		return err
	}
	count, err := p.db.CountVotingProcesses(orgAddress, db.DraftOnly)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	if count >= MaxDraftsPerOrg {
		return errors.ErrMaxDraftsReached.Withf("(%d)", MaxDraftsPerOrg)
	}
	return nil
}

// OrgCanAddNMembers checks that memberNumber more members keep the organization's memberbase
// within MaxMembersPerOrg.
func (p *Subscriptions) OrgCanAddNMembers(orgAddress common.Address, memberNumber int) error {
	count, err := p.db.CountOrgMembers(orgAddress)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	if count+int64(memberNumber) > MaxMembersPerOrg {
		return errors.ErrExceedsOrganizationMembersLimit.Withf("(%d)", MaxMembersPerOrg)
	}
	return nil
}

// OrgCanPublishProcess enforces, synchronously, what HasTxPermission enforces when the
// transaction is built, so the /processes publish path surfaces it as a 400 (and in the
// dry-run) instead of as an opaque async job failure. Admin role is verified by the caller.
func (p *Subscriptions) OrgCanPublishProcess(org *db.Organization, durationSeconds uint32) error {
	if err := p.ownsProcesses(org); err != nil {
		return err
	}
	return durationAllowed(durationSeconds)
}

// IsIntegrator reports whether the organization is enabled as an integrator: flagged as one
// (by an operator, or at creation), and not itself managed by another integrator.
func (*Subscriptions) IsIntegrator(org *db.Organization) bool {
	return org != nil && org.Integrator && !managed(org)
}

// CanCreateManagedOrg checks that the organization may create managed organizations. Being an
// integrator is all it takes: there is no limit on how many.
func (p *Subscriptions) CanCreateManagedOrg(integrator *db.Organization) error {
	if !p.IsIntegrator(integrator) {
		return errors.ErrNotAnIntegrator
	}
	return nil
}
