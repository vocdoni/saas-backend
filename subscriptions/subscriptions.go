// Package subscriptions provides functionality for managing organization subscriptions
// and enforcing permissions based on subscription plans.
package subscriptions

import (
	stderrors "errors"
	"fmt"

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
	// CreateDraft represents the permission to create draft processes.
	CreateDraft
	// CreateOrg represents the permission to create new organizations.
	CreateOrg
)

const MaxOrgsPerUser = 15

// String returns the string representation of the DBPermission.
func (p DBPermission) String() string {
	switch p {
	case InviteUser:
		return "InviteUser"
	case DeleteUser:
		return "DeleteUser"
	case CreateSubOrg:
		return "CreateSubOrg"
	case CreateDraft:
		return "CreateDraft"
	case CreateOrg:
		return "CreateOrg"
	default:
		return "Unknown"
	}
}

// DBInterface defines the database methods required by the Subscriptions service
type DBInterface interface {
	Plan(id string) (*db.Plan, error)
	UserByEmail(email string) (*db.User, error)
	Organization(address common.Address) (*db.Organization, error)
	OrganizationWithParent(address common.Address) (*db.Organization, *db.Organization, error)
	CountCensusParticipants(censusID string) (int64, error)
	CountOrgMembers(orgAddress common.Address) (int64, error)
	CountMembersManagedBy(integratorAddr common.Address) (int64, error)
	SumSentEmailsManagedBy(integratorAddr common.Address) (int, error)
	SumSentSMSManagedBy(integratorAddr common.Address) (int, error)
	CountProcesses(orgAddress common.Address, draft db.DraftFilter) (int64, error)
	OrganizationMemberGroup(groupID string, orgAddress common.Address) (*db.OrganizationMemberGroup, error)
}

// Subscriptions is the service that manages the organization permissions based on
// the subscription plans.
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

// hasElectionMetadataPermissions checks if the organization has permission to create an election with the given metadata.
func hasElectionMetadataPermissions(process *models.NewProcessTx, plan *db.Plan) (bool, error) {
	// check ANONYMOUS
	if process.Process.EnvelopeType.Anonymous && !plan.Features.Anonymous {
		return false, fmt.Errorf("anonymous elections are not allowed")
	}

	// check WEIGHTED
	if process.Process.EnvelopeType.CostFromWeight && !plan.VotingTypes.Weighted {
		return false, fmt.Errorf("weighted elections are not allowed")
	}

	// check VOTE OVERWRITE
	if process.Process.VoteOptions.MaxVoteOverwrites > 0 && !plan.Features.Overwrite {
		return false, fmt.Errorf("vote overwrites are not allowed")
	}

	// check PROCESS DURATION
	duration := plan.Organization.MaxDuration * 24 * 60 * 60
	if process.Process.Duration > uint32(duration) {
		return false, fmt.Errorf("duration is greater than the allowed")
	}

	// TODO:future check if the election voting type is supported by the plan
	// TODO:future check if the streamURL is used and allowed by the plan

	return true, nil
}

// managed reports whether org is a managed organization (created and owned by an integrator).
func managed(org *db.Organization) bool {
	return org != nil && org.ManagedBy != (common.Address{})
}

// limitsOwner returns the organization whose subscription plan governs usage limits for
// org, together with that plan. For a managed organization the owner is its integrator
// (org.ManagedBy); otherwise it is org itself. It fails closed if a managed org's
// integrator cannot be resolved or has no plan — there is no silent fallback to the
// managed org's own (throwaway default) plan.
func (p *Subscriptions) limitsOwner(org *db.Organization) (*db.Organization, *db.Plan, error) {
	if org == nil {
		return nil, nil, errors.ErrInvalidData.With("organization is nil")
	}
	owner := org
	if managed(org) {
		integrator, err := p.db.Organization(org.ManagedBy)
		if err != nil {
			if stderrors.Is(err, db.ErrNotFound) {
				return nil, nil, errors.ErrOrganizationNotFound.WithErr(err)
			}
			return nil, nil, errors.ErrGenericInternalServerError.WithErr(err)
		}
		owner = integrator
	}
	if owner.Subscription.PlanID == "" {
		return nil, nil, errors.ErrOrganizationHasNoSubscription
	}
	plan, err := p.db.Plan(owner.Subscription.PlanID)
	if err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			return nil, nil, errors.ErrPlanNotFound.WithErr(err)
		}
		return nil, nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	return owner, plan, nil
}

// HasTxPermission checks if the organization has permission to perform the given transaction.
func (p *Subscriptions) HasTxPermission(
	tx *models.Tx,
	txType models.TxType,
	org *db.Organization,
	user *db.User,
) (bool, error) {
	if org == nil {
		return false, errors.ErrInvalidData.With("organization is nil")
	}

	// Resolve the plan that governs this org's limits: the integrator's plan for a
	// managed org, otherwise the org's own plan.
	_, plan, err := p.limitsOwner(org)
	if err != nil {
		return false, err
	}

	switch txType {
	// check UPDATE ACCOUNT INFO
	case models.TxType_SET_ACCOUNT_INFO_URI:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) {
			return false, errors.ErrUserHasNoAdminRole
		}
	// check CREATE PROCESS
	case models.TxType_NEW_PROCESS, models.TxType_SET_PROCESS_CENSUS:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) {
			return false, errors.ErrUserHasNoAdminRole
		}
		newProcess := tx.GetNewProcess()
		// For managed orgs the census-size and process-count limits are enforced against
		// the integrator's aggregate quota (ReserveManagedPublish) at publish time, so the
		// per-org plan checks are skipped here. Capability/duration checks below still apply,
		// using the integrator's plan.
		if !managed(org) {
			if newProcess.Process.MaxCensusSize > uint64(plan.Organization.MaxCensus) {
				return false, errors.ErrProcessCensusSizeExceedsPlanLimit.Withf("plan max census: %d", plan.Organization.MaxCensus)
			}
			if org.Counters.Processes >= plan.Organization.MaxProcesses {
				// allow processes with less than TestMaxCensusSize for user testing
				if newProcess.Process.MaxCensusSize > uint64(db.TestMaxCensusSize) {
					return false, errors.ErrMaxProcessesReached
				}
			}
		}
		return hasElectionMetadataPermissions(newProcess, plan)

	case models.TxType_SET_PROCESS_STATUS,
		models.TxType_CREATE_ACCOUNT:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
			return false, errors.ErrUserHasNoAdminRole
		}
	default:
		return false, fmt.Errorf("unsupported txtype")
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

// OrgHasPermission checks if the org has permission to perform the given action
func (p *Subscriptions) OrgHasPermission(orgAddress common.Address, permission DBPermission) error {
	switch permission {
	case CreateDraft:
		// Check if the organization has a subscription
		org, err := p.db.Organization(orgAddress)
		if err != nil {
			return errors.ErrOrganizationNotFound.WithErr(err)
		}

		// MaxDrafts value comes from the integrator's plan for managed orgs; the draft
		// count itself stays per-org.
		_, plan, err := p.limitsOwner(org)
		if err != nil {
			return err
		}

		count, err := p.db.CountProcesses(orgAddress, db.DraftOnly)
		if err != nil {
			return errors.ErrGenericInternalServerError.WithErr(err)
		}

		if count >= int64(plan.Organization.MaxDrafts) {
			return errors.ErrMaxDraftsReached.Withf("(%d)", plan.Organization.MaxDrafts)
		}
		return nil
	default:
		return fmt.Errorf("permission not found")
	}
}

func (p *Subscriptions) OrgCanAddNMembers(orgAddress common.Address, memberNumber int) error {
	org, err := p.db.Organization(orgAddress)
	if err != nil {
		return errors.ErrOrganizationNotFound.WithErr(err)
	}

	owner, plan, err := p.limitsOwner(org)
	if err != nil {
		return err
	}

	// For a managed org the member limit is a shared pool across all of the integrator's
	// managed orgs; for a standalone org it is just the org's own members.
	var count int64
	if managed(org) {
		count, err = p.db.CountMembersManagedBy(owner.Address)
	} else {
		count, err = p.db.CountOrgMembers(orgAddress)
	}
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}

	if count+int64(memberNumber) > int64(plan.Organization.MaxCensus) {
		return errors.ErrExceedsOrganizationMembersLimit.Withf("(%d)", plan.Organization.MaxCensus)
	}
	return nil
}

func (p *Subscriptions) OrgCanPublishGroupCensus(census *db.Census, groupID string) error {
	org, err := p.db.Organization(census.OrgAddress)
	if err != nil {
		return errors.ErrOrganizationNotFound.WithErr(err)
	}

	owner, plan, err := p.limitsOwner(org)
	if err != nil {
		return err
	}

	group, err := p.db.OrganizationMemberGroup(groupID, org.Address)
	if err != nil {
		return errors.ErrGroupNotFound.WithErr(err)
	}

	memberCount := len(group.MemberIDs)
	if group.IsAutoGroup {
		count, err := p.db.CountOrgMembers(org.Address)
		if err != nil {
			return errors.ErrGenericInternalServerError.WithErr(err)
		}
		memberCount = int(count)
	}

	// Only check (and, for managed orgs, aggregate) the 2FA channels the census actually
	// requests. For a managed org the allowance is a shared pool summed across all of the
	// integrator's managed orgs; for a standalone org it is the org's own sent counter.
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldEmail) {
		sentEmails := org.Counters.SentEmails
		if managed(org) {
			if sentEmails, err = p.db.SumSentEmailsManagedBy(owner.Address); err != nil {
				return errors.ErrGenericInternalServerError.WithErr(err)
			}
		}
		if remainingEmails := max(0, plan.Features.TwoFaEmail-sentEmails); memberCount > remainingEmails {
			return errors.ErrProcessCensusSizeExceedsEmailAllowance.Withf("remaining emails: %d", remainingEmails)
		}
	}

	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) {
		sentSMS := org.Counters.SentSMS
		if managed(org) {
			if sentSMS, err = p.db.SumSentSMSManagedBy(owner.Address); err != nil {
				return errors.ErrGenericInternalServerError.WithErr(err)
			}
		}
		if remainingSMS := max(0, plan.Features.TwoFaSms-sentSMS); memberCount > remainingSMS {
			return errors.ErrProcessCensusSizeExceedsSMSAllowance.Withf("remaining sms: %d", remainingSMS)
		}
	}

	return nil
}

func (p *Subscriptions) OrgCanAddCensusParticipants(orgAddress common.Address, censusID string, participantsCount int) error {
	org, err := p.db.Organization(orgAddress)
	if err != nil {
		return errors.ErrOrganizationNotFound.WithErr(err)
	}

	// Per-census size is bounded by the governing plan's MaxCensus: the integrator's plan
	// for a managed org, otherwise the org's own. This keeps the participant-add path
	// bounded (the integrator-wide ManagedCensusSize total is additionally reserved at
	// publish) rather than relying on the publish-time check alone.
	_, plan, err := p.limitsOwner(org)
	if err != nil {
		return err
	}

	count, err := p.db.CountCensusParticipants(censusID)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}

	if count+int64(participantsCount) > int64(plan.Organization.MaxCensus) {
		return errors.ErrProcessCensusSizeExceedsPlanLimit.Withf("(%d)", plan.Organization.MaxCensus)
	}
	return nil
}

// IsIntegrator reports whether the given organization is enabled as an integrator.
//
// Enablement is derived entirely from integrator limits — there is no separate flag:
//   - a per-organization IntegratorLimits override (the manual/admin path) grants
//     integrator status regardless of subscription state; and
//   - otherwise the organization's subscription plan grants it, but only while the
//     subscription is active.
//
// In both cases "integrator" means the effective limits allow at least one managed org.
func (p *Subscriptions) IsIntegrator(org *db.Organization) bool {
	if org == nil {
		return false
	}
	if org.IntegratorLimits != nil {
		return org.IntegratorLimits.MaxManagedOrgs > 0
	}
	if !org.Subscription.Active || org.Subscription.PlanID == "" {
		return false
	}
	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil || plan == nil {
		return false
	}
	return plan.IntegratorLimits.MaxManagedOrgs > 0
}

// EffectiveIntegratorLimits returns the integrator limits in force for the org: the
// per-organization override if set, otherwise the limits granted by its plan.
func (p *Subscriptions) EffectiveIntegratorLimits(org *db.Organization) (db.IntegratorLimits, error) {
	if org == nil {
		return db.IntegratorLimits{}, errors.ErrNotAnIntegrator
	}
	if org.IntegratorLimits != nil {
		return *org.IntegratorLimits, nil
	}
	if org.Subscription.PlanID == "" {
		return db.IntegratorLimits{}, errors.ErrPlanNotFound.With("organization has no subscription plan")
	}
	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil {
		return db.IntegratorLimits{}, fmt.Errorf("could not get subscription plan: %w", err)
	}
	return plan.IntegratorLimits, nil
}

// CanCreateManagedOrg checks that the integrator may create another managed organization.
func (p *Subscriptions) CanCreateManagedOrg(integrator *db.Organization) error {
	if !p.IsIntegrator(integrator) {
		return errors.ErrNotAnIntegrator
	}
	limits, err := p.EffectiveIntegratorLimits(integrator)
	if err != nil {
		return err
	}
	if integrator.Counters.ManagedOrgs >= limits.MaxManagedOrgs {
		return errors.ErrMaxManagedOrgsReached.Withf("limit %d", limits.MaxManagedOrgs)
	}
	return nil
}

// ManagedPublishLimits returns the integrator's aggregate caps for publishing under its
// managed organizations: the integrator plan's top-level process and census-size limits.
// These bound the ManagedProcesses / ManagedCensusSize counters across all managed orgs.
func (p *Subscriptions) ManagedPublishLimits(integrator *db.Organization) (maxProcesses, maxCensus int, err error) {
	if !p.IsIntegrator(integrator) {
		return 0, 0, errors.ErrNotAnIntegrator
	}
	if integrator.Subscription.PlanID == "" {
		return 0, 0, errors.ErrPlanNotFound.With("integrator has no subscription plan")
	}
	plan, err := p.db.Plan(integrator.Subscription.PlanID)
	if err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			return 0, 0, errors.ErrPlanNotFound.WithErr(err)
		}
		return 0, 0, errors.ErrGenericInternalServerError.WithErr(err)
	}
	return plan.Organization.MaxProcesses, plan.Organization.MaxCensus, nil
}

// CanPublishForManagedOrg checks the integrator's aggregate process/census quota
// before publishing an election (with the given census size) under a managed org.
func (p *Subscriptions) CanPublishForManagedOrg(integrator *db.Organization, censusSize int) error {
	maxProcesses, maxCensus, err := p.ManagedPublishLimits(integrator)
	if err != nil {
		return err
	}
	if integrator.Counters.ManagedProcesses >= maxProcesses {
		return errors.ErrIntegratorQuotaExceeded.Withf("max managed processes %d", maxProcesses)
	}
	if integrator.Counters.ManagedCensusSize+censusSize > maxCensus {
		return errors.ErrIntegratorQuotaExceeded.Withf("max managed census size %d", maxCensus)
	}
	return nil
}
