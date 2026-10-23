package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
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
// entirely (members, groups, censuses, participants, processes, bundles, voting
// processes, questions, API keys, CSP tokens, jobs, invitations); in organizations with other admins only the
// user's membership is removed. Invitations addressed to or issued by the
// user, their verification codes and the user document itself are deleted.
// Each step is best-effort: failures are accumulated and returned joined, so a
// single stuck collection cannot leave the erasure silently half-done. It refuses,
// erasing nothing, while an organization it would tear down still has drafts: a
// draft may hold money only the API layer can refund (api.DeleteOrgDrafts).
// Payment rows are kept: they are the audit trail of money that moved.
func (ms *MongoStorage) EraseUser(userID uint64) (*UserErasureReport, error) {
	user, err := ms.User(userID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch user: %w", err)
	}
	// refuse before erasing anything rather than strand a draft's money
	for _, userOrg := range user.Organizations {
		soleAdmin, err := ms.IsSoleOrgAdmin(userOrg.Address, user.ID)
		if err != nil {
			return nil, fmt.Errorf("classifying org %s: %w", userOrg.Address, err)
		}
		if !soleAdmin {
			continue
		}
		drafts, err := ms.CountVotingProcesses(userOrg.Address, DraftOnly)
		if err != nil {
			return nil, fmt.Errorf("counting drafts of org %s: %w", userOrg.Address, err)
		}
		if drafts > 0 {
			return nil, fmt.Errorf("org %s still has %d drafts: delete them (refunding the paid ones) first",
				userOrg.Address, drafts)
		}
	}
	report := &UserErasureReport{UserID: user.ID, Email: user.Email}
	var errs []error
	for _, userOrg := range user.Organizations {
		soleAdmin, err := ms.IsSoleOrgAdmin(userOrg.Address, user.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("classifying org %s: %w", userOrg.Address, err))
			continue
		}
		if soleAdmin {
			if err := ms.EraseOrganizationData(userOrg.Address); err != nil {
				errs = append(errs, fmt.Errorf("tearing down org %s: %w", userOrg.Address, err))
			}
			report.DeletedOrgs = append(report.DeletedOrgs, userOrg.Address)
			continue
		}
		removed, err := ms.RemoveOrganizationUser(userOrg.Address, user.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing membership in org %s: %w", userOrg.Address, err))
		}
		if removed {
			if err := ms.DecrementOrganizationUsersCounter(userOrg.Address); err != nil {
				errs = append(errs, fmt.Errorf("decrementing users counter of org %s: %w", userOrg.Address, err))
			}
		}
		report.KeptOrgs = append(report.KeptOrgs, userOrg.Address)
		if org, err := ms.Organization(userOrg.Address); err == nil && org.Creator == user.Email {
			report.CreatorEmailRetained = append(report.CreatorEmailRetained, userOrg.Address)
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

// EraseOrganizationData tears down every collection owned by the organization (including the
// /processes voting processes, questions and API keys, and their CSP tokens and status) and
// finally the organization document itself. It is the single cascade shared by user erasure and
// the managed-organization deletion handler; callers run any on-chain election guards beforehand
// (erasure is an operator decision, not an API call). Failures are accumulated
// so one stuck collection does not abort the rest of the teardown.
func (ms *MongoStorage) EraseOrganizationData(address common.Address) error {
	if address.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}
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
	// /processes: CSP tokens are anchored on the voting process id (ObjectID bytes) and the CSP
	// status is keyed by each question's upstream id. List before deleting what the listing reads.
	votingIDs, err := ms.VotingProcessIDsByOrg(address)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing voting processes: %w", err))
	}
	for _, id := range votingIDs {
		if _, err := ms.DeleteCSPAuthByAnchor(internal.HexBytes(id[:])); err != nil {
			errs = append(errs, fmt.Errorf("deleting CSP auth tokens of voting process %s: %w", id.Hex(), err))
		}
	}
	upstreamIDs, err := ms.QuestionUpstreamIDsByOrg(address)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing question upstream ids: %w", err))
	}
	for _, id := range upstreamIDs {
		if _, err := ms.DeleteCSPProcessByProcess(id); err != nil {
			errs = append(errs, fmt.Errorf("deleting CSP status of question %s: %w", id.String(), err))
		}
	}
	if _, err := ms.DeleteQuestionsByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting questions: %w", err))
	}
	if _, err := ms.DeleteVotingProcessesByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting voting processes: %w", err))
	}
	if _, err := ms.DeleteAPIKeysByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting API keys: %w", err))
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

// OrgDataSummary counts the data an organization teardown would remove.
type OrgDataSummary struct {
	Members, Groups, Censuses, Participants                   int64
	Processes, Bundles, Invitations                           int64
	VotingProcesses, Drafts, Questions, ActiveQuestions, Keys int64
}

// OrganizationDataSummary counts the data EraseOrganizationData would remove.
// ActiveQuestions are those whose stored status is READY or PAUSED (possibly live on-chain).
func (ms *MongoStorage) OrganizationDataSummary(address common.Address) (*OrgDataSummary, error) {
	s := &OrgDataSummary{}
	var err error
	if s.Members, err = ms.CountOrgMembers(address); err != nil {
		return nil, fmt.Errorf("could not count members of org %s: %w", address, err)
	}
	if s.Groups, _, err = ms.OrganizationMemberGroups(address, 1, 1); err != nil {
		return nil, fmt.Errorf("could not count groups of org %s: %w", address, err)
	}
	censuses, err := ms.CensusesByOrg(address)
	if err != nil {
		return nil, fmt.Errorf("could not list censuses of org %s: %w", address, err)
	}
	s.Censuses = int64(len(censuses))
	for _, census := range censuses {
		n, err := ms.CountCensusParticipants(census.ID.Hex())
		if err != nil {
			return nil, fmt.Errorf("could not count participants of census %s: %w", census.ID.Hex(), err)
		}
		s.Participants += n
	}
	if s.Processes, err = ms.CountProcesses(address, AllProcesses); err != nil {
		return nil, fmt.Errorf("could not count processes of org %s: %w", address, err)
	}
	bundles, err := ms.ProcessBundlesByOrg(address)
	if err != nil {
		return nil, fmt.Errorf("could not list bundles of org %s: %w", address, err)
	}
	s.Bundles = int64(len(bundles))
	invites, err := ms.PendingInvitations(address)
	if err != nil {
		return nil, fmt.Errorf("could not list invitations of org %s: %w", address, err)
	}
	s.Invitations = int64(len(invites))
	if s.VotingProcesses, err = ms.CountVotingProcesses(address, AllProcesses); err != nil {
		return nil, fmt.Errorf("could not count voting processes of org %s: %w", address, err)
	}
	if s.Drafts, err = ms.CountVotingProcesses(address, DraftOnly); err != nil {
		return nil, fmt.Errorf("could not count drafts of org %s: %w", address, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if s.Questions, err = ms.processesQuestions.CountDocuments(ctx, bson.M{"orgAddress": address}); err != nil {
		return nil, fmt.Errorf("could not count questions of org %s: %w", address, err)
	}
	if s.ActiveQuestions, err = ms.processesQuestions.CountDocuments(ctx, bson.M{
		"orgAddress": address,
		"status":     bson.M{"$in": []string{QuestionStatusReady, QuestionStatusPaused}},
	}); err != nil {
		return nil, fmt.Errorf("could not count active questions of org %s: %w", address, err)
	}
	keys, err := ms.APIKeysByOrg(address)
	if err != nil {
		return nil, fmt.Errorf("could not list api keys of org %s: %w", address, err)
	}
	s.Keys = int64(len(keys))
	return s, nil
}
