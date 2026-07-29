package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
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
	// organization signing key is derived from secret+creator+nonce (see
	// account.OrganizationSigner), so redacting it would change the derived
	// key and break the organization's on-chain account.
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
// entirely (members, groups, censuses, participants, processes, bundles, CSP
// tokens, jobs, invitations); in organizations with other admins only the
// user's membership is removed. Invitations addressed to or issued by the
// user, their verification codes and the user document itself are deleted.
// Each step is best-effort: failures are accumulated and returned joined, so a
// single stuck collection cannot leave the erasure silently half-done.
func (ms *MongoStorage) EraseUser(userID uint64) (*UserErasureReport, error) {
	user, err := ms.User(userID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch user: %w", err)
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
			if err := ms.eraseOrgData(userOrg.Address); err != nil {
				errs = append(errs, fmt.Errorf("tearing down org %s: %w", userOrg.Address, err))
			}
			report.DeletedOrgs = append(report.DeletedOrgs, userOrg.Address)
			continue
		}
		if err := ms.RemoveOrganizationUser(userOrg.Address, user.ID); err != nil {
			errs = append(errs, fmt.Errorf("removing membership in org %s: %w", userOrg.Address, err))
		}
		if err := ms.DecrementOrganizationUsersCounter(userOrg.Address); err != nil {
			errs = append(errs, fmt.Errorf("decrementing users counter of org %s: %w", userOrg.Address, err))
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
	_, err := ms.verifications.DeleteMany(ctx, bson.M{"_id": userID})
	return err
}

// eraseOrgData tears down every collection owned by the organization and
// finally the organization document itself. It mirrors the cascade used by the
// managed-organization deletion handler, minus the on-chain election guards
// (erasure is an operator decision, not an API call). Failures are accumulated
// so one stuck collection does not abort the rest of the teardown.
func (ms *MongoStorage) eraseOrgData(address common.Address) error {
	var errs []error
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
		if _, err := ms.DeleteCSPAuthByBundle(*bundleID); err != nil {
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
	if _, err := ms.DeleteAllOrgMemberGroups(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting member groups: %w", err))
	}
	if _, err := ms.DeleteAllOrgMembers(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting members: %w", err))
	}
	if _, err := ms.DeleteJobsByOrg(address); err != nil {
		errs = append(errs, fmt.Errorf("deleting jobs: %w", err))
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
