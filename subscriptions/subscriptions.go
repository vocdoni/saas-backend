package subscriptions

import (
	"fmt"
	"strconv"

	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/proto/build/go/models"
)

// SubscriptionsConfig holds the configuration for the subscriptions service.
// It includes a reference to the MongoDB storage used by the service.
type SubscriptionsConfig struct {
	DB *db.MongoStorage
}

// OrgPermission represents the permissions that an organization can have.
type DBPermission int

const (
	InviteMember DBPermission = iota
	DeleteMember
	CreateSubOrg
	CreateDraft
)

// String returns the string representation of the DBPermission.
func (p DBPermission) String() string {
	switch p {
	case InviteMember:
		return "InviteMember"
	case DeleteMember:
		return "DeleteMember"
	case CreateSubOrg:
		return "CreateSubOrg"
	case CreateDraft:
		return "CreateDraft"
	default:
		return "Unknown"
	}
}

// Subscriptions is the service that manages the organization permissions based on
// the subscription plans.
type Subscriptions struct {
	db *db.MongoStorage
}

// New creates a new Subscriptions service with the given configuration.
func New(conf *SubscriptionsConfig) *Subscriptions {
	if conf == nil {
		return nil
	}
	return &Subscriptions{
		db: conf.DB,
	}
}

// hasElectionMetadataPermissions checks if the organization has permission to create an election with the given metadata.
func (p *Subscriptions) hasElectionMetadataPermissions(process *models.NewProcessTx, plan *db.Plan) (bool, error) {
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
	duration, err := daysDurationToSeconds(plan.Organization.MaxDuration)
	if err != nil {
		return false, fmt.Errorf("could not convert duration to seconds: %v", err)
	}
	if process.Process.Duration > duration {
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
	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil {
		return false, fmt.Errorf("could not get organization plan: %v", err)
	}

	switch txType {
	// check UPDATE ACCOUNT INFO
	case models.TxType_SET_ACCOUNT_INFO_URI:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) {
			return false, fmt.Errorf("user does not have admin role")
		}

	// check CREATE PROCESS
	case models.TxType_NEW_PROCESS, models.TxType_SET_PROCESS_CENSUS:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) {
			return false, fmt.Errorf("user does not have admin role")
		}
		newProcess := tx.GetNewProcess()
		if newProcess.Process.MaxCensusSize > uint64(org.Subscription.MaxCensusSize) {
			return false, fmt.Errorf("MaxCensusSize is greater than the allowed")
		}
		if org.Counters.Processes >= plan.Organization.MaxProcesses {
			return false, fmt.Errorf("max processes reached")
		}
		return p.hasElectionMetadataPermissions(newProcess, plan)

	// check SET_PROCESS
	case models.TxType_SET_PROCESS_STATUS:
		// check if the user has the admin role for the organization
		if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
			return false, fmt.Errorf("user does not have admin role")
		}

	}

	return true, nil
}

// HasDBPersmission checks if the user has permission to perform the given action in the organization stored in the DB
func (p *Subscriptions) HasDBPersmission(userEmail, orgAddress string, permission DBPermission) (bool, error) {
	user, err := p.db.UserByEmail(userEmail)
	if err != nil {
		return false, fmt.Errorf("could not get user: %v", err)
	}
	org, _, err := p.db.Organization(orgAddress, false)
	if err != nil {
		return false, fmt.Errorf("could not get organization: %v", err)
	}
	plan, err := p.db.Plan(org.Subscription.PlanID)
	if err != nil {
		return false, fmt.Errorf("could not get organization plan: %v", err)
	}
	switch permission {
	case InviteMember:
		// check if the user has permission to invite members
		if !user.HasRoleFor(orgAddress, db.AdminRole) {
			return false, fmt.Errorf("user does not have admin role")
		}
		if org.Counters.Members >= plan.Organization.Members {
			return false, fmt.Errorf("max members reached")
		}
		return true, nil
	case DeleteMember:
		// check if the user has permission to delete members
		if !user.HasRoleFor(orgAddress, db.AdminRole) {
			return false, fmt.Errorf("user does not have admin role")
		}
		return true, nil
	case CreateSubOrg:
		// check if the user has permission to create sub organizations
		if !user.HasRoleFor(orgAddress, db.AdminRole) {
			return false, fmt.Errorf("user does not have admin role")
		}
		if org.Counters.SubOrgs >= plan.Organization.SubOrgs {
			return false, fmt.Errorf("max sub organizations reached")
		}
		return true, nil
	}
	return false, fmt.Errorf("permission not found")
}

// In the plan the duration is given in a string
func daysDurationToSeconds(duration string) (uint32, error) {
	num, err := strconv.ParseUint(duration, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(num * 24 * 60 * 60), nil
}
