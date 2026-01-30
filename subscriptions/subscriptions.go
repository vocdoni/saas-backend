// Package subscriptions provides functionality for managing organization subscriptions
// and enforcing permissions based on subscription plans.
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
	// CreateDraft represents the permission to create draft processes.
	CreateDraft
)

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
	default:
		return "Unknown"
	}
}

// DBInterface defines the database methods required by the Subscriptions service
type DBInterface interface {
	Plan(id uint64) (*db.Plan, error)
	UserByEmail(email string) (*db.User, error)
	Organization(address common.Address) (*db.Organization, error)
	OrganizationWithParent(address common.Address) (*db.Organization, *db.Organization, error)
	CountProcesses(orgAddress common.Address, draft db.DraftFilter) (int64, error)
	OrganizationMemberGroup(groupID string, orgAddress common.Address) (*db.OrganizationMemberGroup, error)
	GetUsageSnapshot(orgAddress common.Address, periodStart time.Time) (*db.UsageSnapshot, error)
	UpsertUsageSnapshot(snapshot *db.UsageSnapshot) error
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

	// Check if the organization has a subscription
	if org.Subscription.PlanID == 0 {
		return false, errors.ErrOrganizationHasNoSubscription
	}

	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil {
		return false, errors.ErrPlanNotFound.WithErr(err)
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
		if newProcess.Process.MaxCensusSize > uint64(plan.Organization.MaxCensus) {
			return false, errors.ErrProcessCensusSizeExceedsPlanLimit.Withf("plan max census: %d", plan.Organization.MaxCensus)
		}
		usage, ok, err := p.PeriodUsage(org)
		if err != nil {
			return false, errors.ErrGenericInternalServerError.WithErr(err)
		}

		usedProcesses := org.Counters.Processes
		if ok {
			usedProcesses = usage.Processes
		}

		if usedProcesses >= plan.Organization.MaxProcesses {
			// allow processes with less than TestMaxCensusSize for user testing
			if newProcess.Process.MaxCensusSize > uint64(db.TestMaxCensusSize) {
				return false, errors.ErrMaxProcessesReached
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

		if org.Subscription.PlanID == 0 {
			return errors.ErrOrganizationHasNoSubscription.With("can't create draft process")
		}

		plan, err := p.db.Plan(org.Subscription.PlanID)
		if err != nil {
			return errors.ErrGenericInternalServerError.WithErr(err)
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

func (p *Subscriptions) OrgCanPublishGroupCensus(census *db.Census, groupID string) error {
	org, err := p.db.Organization(census.OrgAddress)
	if err != nil {
		return errors.ErrOrganizationNotFound.WithErr(err)
	}

	if org.Subscription.PlanID == 0 {
		return errors.ErrOrganizationHasNoSubscription
	}

	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil {
		return errors.ErrPlanNotFound.WithErr(err)
	}

	group, err := p.db.OrganizationMemberGroup(groupID, org.Address)
	if err != nil {
		return errors.ErrGroupNotFound.WithErr(err)
	}

	usage, ok, err := p.PeriodUsage(org)
	if err != nil {
		return errors.ErrGenericInternalServerError.WithErr(err)
	}
	sentEmails := org.Counters.SentEmails
	sentSMS := org.Counters.SentSMS
	if ok {
		sentEmails = usage.SentEmails
		sentSMS = usage.SentSMS
	}

	remainingEmails := plan.Organization.MaxSentEmails - sentEmails
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldEmail) && len(group.MemberIDs) > remainingEmails {
		return errors.ErrProcessCensusSizeExceedsEmailAllowance.Withf("remaining emails: %d", remainingEmails)
	}
	remainingSMS := plan.Organization.MaxSentSMS - sentSMS
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) && len(group.MemberIDs) > remainingSMS {
		return errors.ErrProcessCensusSizeExceedsSMSAllowance.Withf("remaining sms: %d", remainingSMS)
	}

	return nil
}

func (p *Subscriptions) PeriodUsage(org *db.Organization) (db.OrganizationCounters, bool, error) {
	if org == nil {
		return db.OrganizationCounters{}, false, errors.ErrInvalidData
	}

	periodStart, periodEnd, ok := db.ComputeAnnualPeriod(
		org.Subscription,
		org.Subscription.BillingPeriod,
		time.Now(),
	)
	if !ok {
		return db.OrganizationCounters{}, false, nil
	}

	snapshot, err := p.db.GetUsageSnapshot(org.Address, periodStart)
	if err != nil {
		if err == db.ErrNotFound {
			snapshot = &db.UsageSnapshot{
				OrgAddress:    org.Address,
				PeriodStart:   periodStart,
				PeriodEnd:     periodEnd,
				BillingPeriod: org.Subscription.BillingPeriod,
				Baseline: db.UsageSnapshotBaseline{
					Processes:  org.Counters.Processes,
					SentSMS:    org.Counters.SentSMS,
					SentEmails: org.Counters.SentEmails,
				},
			}
			if err := p.db.UpsertUsageSnapshot(snapshot); err != nil {
				return db.OrganizationCounters{}, false, err
			}
			return db.OrganizationCounters{}, true, nil
		}
		return db.OrganizationCounters{}, false, err
	}

	return db.OrganizationCounters{
		Processes:  clampCounter(org.Counters.Processes - snapshot.Baseline.Processes),
		SentSMS:    clampCounter(org.Counters.SentSMS - snapshot.Baseline.SentSMS),
		SentEmails: clampCounter(org.Counters.SentEmails - snapshot.Baseline.SentEmails),
	}, true, nil
}

func clampCounter(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
