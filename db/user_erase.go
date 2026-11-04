package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// UserErasureReport summarizes what EraseUser removed or kept, so the operator
// running an erasure request has an audit trail of the outcome.
type UserErasureReport struct {
	UserID uint64
	Email  string
	// DeletedOrgs are organizations torn down entirely because the user was
	// their sole admin.
	DeletedOrgs []common.Address
	// KeptOrgs are organizations that survive; only the user's membership was
	// removed.
	KeptOrgs []common.Address
	// CreatorEmailRetained lists surviving organizations whose creator field
	// still holds the erased user's email. It is retained deliberately: the
	// organization signing key is derived from secret+signerSeed+nonce (see
	// Organization.SignerSeedValue), where signerSeed is the creator's email at
	// creation time and must never change, and rows without a seed still fall
	// back to the creator field. The email therefore also survives in signerSeed,
	// and redacting either would change the derived key and break the
	// organization's on-chain account.
	CreatorEmailRetained []common.Address
	// ObjectsScrubbed is how many uploaded objects had the user's email removed from their userId.
	ObjectsScrubbed int64
}

// ErasurePlanOrg describes what erasing a user does to one of their organizations.
type ErasurePlanOrg struct {
	Address common.Address
	// Name is the organization's display name, "" when unnamed.
	Name string
	Role UserRole
	// DeletedEntirely is set when the user is the sole admin: the organization is torn down.
	DeletedEntirely bool
	// CreatorEmailRetained is set on a kept organization whose creator is the user's email.
	CreatorEmailRetained bool
	// ManagedBy is the integrator of a managed organization, zero otherwise.
	ManagedBy common.Address
	// The counts are set only when DeletedEntirely.
	LegacyProcesses, Bundles, VotingProcesses, Drafts, Members, Groups,
	Censuses, CensusParticipants, PendingInvitations, APIKeys int64
	// WalletBalanceCents is the balance of the organization's own wallet, kept by the erasure: the
	// stored balance before any drain or draft credit-back (the plan never writes). It is set only
	// when DeletedEntirely, and 0 for managed organizations (their integrator pays).
	WalletBalanceCents int64
	// SurvivingChildren are the organizations whose parent or managedBy is this one and that the
	// erasure does not tear down. Set only when DeletedEntirely.
	SurvivingChildren []common.Address
}

// UserErasurePlan describes what EraseUser would do for a user, without changing anything.
type UserErasurePlan struct {
	UserID      uint64
	Name, Email string
	Orgs        []ErasurePlanOrg
	// Blocked lists the torn-down orgs with SurvivingChildren. EraseUser refuses while it is
	// non-empty. Drafts do not block: they are deleted first by api.DeleteUserOrgDrafts.
	Blocked []common.Address
	// Objects is how many uploaded objects (images, election metadata) carry the user's email as their
	// userId. EraseUser keeps them and only removes the email.
	Objects int64
}

// UserErasurePlan returns what erasing the user would remove and keep, organization by
// organization, in the order of the user's organizations.
func (ms *MongoStorage) UserErasurePlan(userID uint64) (*UserErasurePlan, error) {
	user, err := ms.User(userID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch user: %w", err)
	}
	plan := &UserErasurePlan{
		UserID: user.ID,
		Name:   strings.TrimSpace(user.FirstName + " " + user.LastName),
		Email:  user.Email,
	}
	tornDown := map[common.Address]bool{}
	for _, userOrg := range user.Organizations {
		org, err := ms.Organization(userOrg.Address)
		if err != nil {
			return nil, fmt.Errorf("could not fetch org %s: %w", userOrg.Address, err)
		}
		soleAdmin, err := ms.IsSoleOrgAdmin(userOrg.Address, user.ID)
		if err != nil {
			return nil, fmt.Errorf("could not classify org %s: %w", userOrg.Address, err)
		}
		planOrg := ErasurePlanOrg{
			Address:         userOrg.Address,
			Name:            org.DisplayName(),
			Role:            userOrg.Role,
			DeletedEntirely: soleAdmin,
			ManagedBy:       org.ManagedBy,
		}
		if soleAdmin {
			tornDown[userOrg.Address] = true
			if err := ms.countOrgContents(&planOrg); err != nil {
				return nil, err
			}
		} else {
			planOrg.CreatorEmailRetained = org.Creator == user.Email
		}
		plan.Orgs = append(plan.Orgs, planOrg)
	}
	// a child survives unless the same erasure tears it down
	for i := range plan.Orgs {
		planOrg := &plan.Orgs[i]
		if !planOrg.DeletedEntirely {
			continue
		}
		children, err := ms.childOrganizations(planOrg.Address)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if !tornDown[child] {
				planOrg.SurvivingChildren = append(planOrg.SurvivingChildren, child)
			}
		}
		if len(planOrg.SurvivingChildren) > 0 {
			plan.Blocked = append(plan.Blocked, planOrg.Address)
		}
	}
	if plan.Objects, err = ms.countObjectsByOwner(user.Email); err != nil {
		return nil, err
	}
	return plan, nil
}

// countOrgContents fills the counts and the wallet balance of an organization to be torn down.
func (ms *MongoStorage) countOrgContents(planOrg *ErasurePlanOrg) error {
	address := planOrg.Address
	var err error
	if planOrg.Members, err = ms.CountOrgMembers(address); err != nil {
		return fmt.Errorf("could not count members of org %s: %w", address, err)
	}
	if planOrg.Groups, _, err = ms.OrganizationMemberGroups(address, 1, 1); err != nil {
		return fmt.Errorf("could not count groups of org %s: %w", address, err)
	}
	censuses, err := ms.CensusesByOrg(address)
	if err != nil {
		return fmt.Errorf("could not list censuses of org %s: %w", address, err)
	}
	planOrg.Censuses = int64(len(censuses))
	for _, census := range censuses {
		n, err := ms.CountCensusParticipants(census.ID.Hex())
		if err != nil {
			return fmt.Errorf("could not count participants of census %s: %w", census.ID.Hex(), err)
		}
		planOrg.CensusParticipants += n
	}
	if planOrg.LegacyProcesses, err = ms.CountProcesses(address, AllProcesses); err != nil {
		return fmt.Errorf("could not count processes of org %s: %w", address, err)
	}
	bundles, err := ms.ProcessBundlesByOrg(address)
	if err != nil {
		return fmt.Errorf("could not list bundles of org %s: %w", address, err)
	}
	planOrg.Bundles = int64(len(bundles))
	invites, err := ms.PendingInvitations(address)
	if err != nil {
		return fmt.Errorf("could not list invitations of org %s: %w", address, err)
	}
	planOrg.PendingInvitations = int64(len(invites))
	if planOrg.VotingProcesses, err = ms.CountVotingProcesses(address, AllProcesses); err != nil {
		return fmt.Errorf("could not count voting processes of org %s: %w", address, err)
	}
	if planOrg.Drafts, err = ms.CountVotingProcesses(address, DraftOnly); err != nil {
		return fmt.Errorf("could not count drafts of org %s: %w", address, err)
	}
	keys, err := ms.APIKeysByOrg(address)
	if err != nil {
		return fmt.Errorf("could not list API keys of org %s: %w", address, err)
	}
	planOrg.APIKeys = int64(len(keys))
	if planOrg.ManagedBy == (common.Address{}) {
		if planOrg.WalletBalanceCents, err = ms.storedWalletBalance(address); err != nil {
			return err
		}
	}
	return nil
}

// storedWalletBalance reads the balance stored on the organization's wallet without draining it
// (Wallet would post the unposted movements, a write), so building a plan never writes. It is the
// balance before any drain or draft credit-back; unposted movements are already part of it, since
// each one moves the balance in the same write that queues it. A missing wallet reads 0.
func (ms *MongoStorage) storedWalletBalance(address common.Address) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var wallet struct {
		BalanceCents int64 `bson:"balanceCents"`
	}
	err := ms.wallets.FindOne(ctx, bson.M{"_id": address},
		options.FindOne().SetProjection(bson.M{"balanceCents": 1})).Decode(&wallet)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("could not read the wallet of org %s: %w", address, err)
	}
	return wallet.BalanceCents, nil
}

// childOrganizations returns the organizations whose parent or managedBy is the given one.
func (ms *MongoStorage) childOrganizations(address common.Address) ([]common.Address, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.organizations.Find(ctx, bson.M{"$or": bson.A{
		bson.M{"parent": address}, bson.M{"managedBy": address},
	}}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("could not list the sub-orgs of org %s: %w", address, err)
	}
	var children []struct {
		Address common.Address `bson:"_id"`
	}
	if err := cursor.All(ctx, &children); err != nil {
		return nil, fmt.Errorf("could not decode the sub-orgs of org %s: %w", address, err)
	}
	addresses := make([]common.Address, 0, len(children))
	for _, child := range children {
		addresses = append(addresses, child.Address)
	}
	return addresses, nil
}

// IsSoleOrgAdmin reports whether the user holds the admin role in the
// organization and no other user does.
func (ms *MongoStorage) IsSoleOrgAdmin(address common.Address, userID uint64) (bool, error) {
	users, err := ms.OrganizationUsers(address)
	if err != nil {
		return false, fmt.Errorf("could not get organization users: %w", err)
	}
	userIsAdmin, othersAreAdmin := false, false
	for _, u := range users {
		for _, o := range u.Organizations {
			if o.Address == address && o.Role == AdminRole {
				if u.ID == userID {
					userIsAdmin = true
				} else {
					othersAreAdmin = true
				}
			}
		}
	}
	return userIsAdmin && !othersAreAdmin, nil
}

// EraseUser removes a registered user and their personal data (right to
// erasure). Organizations where the user is the sole admin are torn down
// entirely (members, groups, censuses, participants, processes, voting processes
// and their questions, bundles, CSP tokens and statuses, jobs, metered votes, API keys, invitations); in
// organizations with other admins only the user's membership is removed. Invitations addressed to or
// issued by the user, their verification codes and the user document itself are deleted. The objects
// they uploaded are kept with the email removed from their userId: they hold images and the metadata
// on-chain elections point at, even for torn-down orgs, and being content-addressed one may be shared.
// Each step is best-effort: failures are accumulated and returned joined, so a
// single stuck collection cannot leave the erasure silently half-done. It refuses,
// erasing nothing, while its UserErasurePlan is Blocked (a sub-org or managed org would
// be orphaned), and while an organization it would tear down still has drafts: a
// draft may hold money only the API layer can refund (api.DeleteUserOrgDrafts).
// Payment rows are kept: they are the audit trail of money that moved.
func (ms *MongoStorage) EraseUser(userID uint64) (*UserErasureReport, error) {
	user, err := ms.User(userID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch user: %w", err)
	}
	// refuse before erasing anything rather than orphan a sub-org or strand a draft's money
	plan, err := ms.UserErasurePlan(userID)
	if err != nil {
		return nil, err
	}
	if len(plan.Blocked) > 0 {
		return nil, fmt.Errorf("orgs %v have sub-orgs or managed orgs that would survive them: resolve those first",
			plan.Blocked)
	}
	for _, planOrg := range plan.Orgs {
		if planOrg.DeletedEntirely && planOrg.Drafts > 0 {
			return nil, fmt.Errorf("org %s still has %d drafts: delete them (refunding the paid ones) first",
				planOrg.Address, planOrg.Drafts)
		}
	}
	report := &UserErasureReport{UserID: user.ID, Email: user.Email}
	var errs []error
	// erase what the plan checked, not a fresh classification: an admin change in between must not
	// tear down a kept org unchecked, nor keep a child whose parent goes
	for _, planOrg := range plan.Orgs {
		if planOrg.DeletedEntirely {
			if err := ms.eraseOrgData(planOrg.Address); err != nil {
				errs = append(errs, fmt.Errorf("tearing down org %s: %w", planOrg.Address, err))
			}
			report.DeletedOrgs = append(report.DeletedOrgs, planOrg.Address)
			continue
		}
		removed, err := ms.RemoveOrganizationUser(planOrg.Address, user.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing membership in org %s: %w", planOrg.Address, err))
		}
		if removed {
			if err := ms.DecrementOrganizationUsersCounter(planOrg.Address); err != nil {
				errs = append(errs, fmt.Errorf("decrementing users counter of org %s: %w", planOrg.Address, err))
			}
		}
		report.KeptOrgs = append(report.KeptOrgs, planOrg.Address)
		if planOrg.CreatorEmailRetained {
			report.CreatorEmailRetained = append(report.CreatorEmailRetained, planOrg.Address)
		}
	}
	// invitations addressed to the user or issued by them
	if _, err := ms.DeleteInvitationsByUser(user.ID, user.Email); err != nil {
		errs = append(errs, fmt.Errorf("deleting invitations: %w", err))
	}
	// verification codes of any type
	if err := ms.deleteAllVerificationCodes(user.ID); err != nil {
		errs = append(errs, fmt.Errorf("deleting verification codes: %w", err))
	}
	// uploaded objects (images, election metadata) stamped with the user's email: kept, email removed
	scrubbed, err := ms.scrubObjectsOwner(user.Email)
	if err != nil {
		errs = append(errs, fmt.Errorf("removing the email from objects: %w", err))
	}
	report.ObjectsScrubbed = scrubbed
	// the user document itself (email, names, password hash, oauth data, roles)
	if err := ms.DelUser(user); err != nil {
		errs = append(errs, fmt.Errorf("deleting user document: %w", err))
	}
	return report, errors.Join(errs...)
}

// deleteAllVerificationCodes removes every verification code of the user,
// regardless of type.
func (ms *MongoStorage) deleteAllVerificationCodes(userID uint64) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.verifications.DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// countObjectsByOwner counts the stored objects whose userId is the given user email.
func (ms *MongoStorage) countObjectsByOwner(email string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	n, err := ms.objects.CountDocuments(ctx, bson.M{"userId": email})
	if err != nil {
		return 0, fmt.Errorf("could not count the objects of %s: %w", email, err)
	}
	return n, nil
}

// scrubObjectsOwner empties the userId of every stored object whose userId is the given user email,
// keeping the objects, and returns how many it changed. The userId is never read for authorization.
func (ms *MongoStorage) scrubObjectsOwner(email string) (int64, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	res, err := ms.objects.UpdateMany(ctx, bson.M{"userId": email}, bson.M{"$set": bson.M{"userId": ""}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// eraseOrgData tears down every collection owned by the organization and
// finally the organization document itself. It mirrors the cascade used by the
// managed-organization deletion handler, minus the on-chain election guards
// (erasure is an operator decision, not an API call). Failures are accumulated
// so one stuck collection does not abort the rest of the teardown.
func (ms *MongoStorage) eraseOrgData(address common.Address) error {
	var errs []error
	// a managed organization's spending under its integrator's cap goes with it, as a managed
	// organization's deletion drops it
	if org, err := ms.Organization(address); err != nil {
		errs = append(errs, fmt.Errorf("reading organization: %w", err))
	} else if org.ManagedBy != (common.Address{}) {
		if err := ms.DropManagedOrgSpend(org.ManagedBy, address); err != nil {
			errs = append(errs, fmt.Errorf("dropping managed organization spend: %w", err))
		}
	}
	bundles, err := ms.ProcessBundlesByOrg(address)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing process bundles: %w", err))
	}
	for _, b := range bundles {
		// match the encoding used by csp/handlers parseBundleID (hex-decoded ObjectID bytes)
		bundleID := new(internal.HexBytes)
		if err := bundleID.ParseString(b.ID.Hex()); err != nil {
			errs = append(errs, fmt.Errorf("encoding bundle id %s: %w", b.ID.Hex(), err))
			continue
		}
		if _, err := ms.DeleteCSPAuthByAnchor(*bundleID); err != nil {
			errs = append(errs, fmt.Errorf("deleting CSP auth tokens of bundle %s: %w", b.ID.Hex(), err))
		}
	}
	published, err := ms.AllProcessesByOrg(address, PublishedOnly)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing published processes: %w", err))
	}
	for _, p := range published {
		if p.Address.Equals(nil) {
			continue
		}
		if _, err := ms.DeleteCSPProcessByProcess(p.Address); err != nil {
			errs = append(errs, fmt.Errorf("deleting CSP status of process %s: %w", p.Address.String(), err))
		}
	}
	if _, err := ms.DeleteProcessBundlesByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting process bundles: %w", err))
	}
	censuses, err := ms.CensusesByOrg(address)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing censuses: %w", err))
	}
	for _, c := range censuses {
		if _, err := ms.DeleteCensusParticipantsByCensus(c.ID.Hex()); err != nil {
			errs = append(errs, fmt.Errorf("deleting participants of census %s: %w", c.ID.Hex(), err))
		}
		if err := ms.DelCensus(c.ID.Hex()); err != nil {
			errs = append(errs, fmt.Errorf("deleting census %s: %w", c.ID.Hex(), err))
		}
	}
	if _, err := ms.DeleteProcessesByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting processes: %w", err))
	}
	// the CSP rows of the voting processes go first: they are found through the processes' ids
	if err := ms.DeleteVotingProcessCSPByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting CSP data of voting processes: %w", err))
	}
	// published voting processes and their questions; EraseUser refused while drafts remained
	if _, err := ms.DeleteVotingProcessesByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting voting processes: %w", err))
	}
	if _, err := ms.DeleteAllOrgMemberGroups(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting member groups: %w", err))
	}
	// the emptied questions are ignored on purpose: this is an erasure, so there is no election
	// left to resize.
	if _, _, err := ms.DeleteAllOrgMembers(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting members: %w", err))
	}
	if _, err := ms.DeleteJobsByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting jobs: %w", err))
	}
	if _, err := ms.DeleteMeteredVotesByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting metered votes: %w", err))
	}
	if _, err := ms.DeleteAPIKeysByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting API keys: %w", err))
	}
	if _, err := ms.DeleteInvitationsByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting invitations: %w", err))
	}
	if err := ms.RemoveOrganizationFromAllUsers(address); err != nil {
		errs = append(errs, fmt.Errorf("unlinking organization from users: %w", err))
	}
	if err := ms.DelOrganization(&Organization{Address: address}); err != nil {
		errs = append(errs, fmt.Errorf("deleting organization document: %w", err))
	}
	return errors.Join(errs...)
}
