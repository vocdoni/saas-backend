package api

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// censusPropagation is the result of adding members to the censuses a memberbase change reaches:
// the resize jobs a client can poll, and the per-census problems that did not stop the write.
type censusPropagation struct {
	JobIDs []string
	Errors []string
}

// propagateMembersToCensuses adds members to every given census and raises the maxCensusSize of the
// published elections built on them.
//
// It is the additive half of keeping a census in step with the memberbase, and it is deliberately
// ordered so that a failure can only ever degrade in one direction:
//
//  1. resolve the target censuses     (read-only)
//  2. plan quota, per census          (read-only) — a refusal here means zero writes
//  3. chain signer preflight          (read-only) — a failure here means zero writes
//  4. the memberbase write            (done by the caller, before calling this)
//  5. participants + census size      (additive and idempotent)
//  6. SET_PROCESS_CENSUS enqueue      (the only surviving failure window)
//
// Steps 2 and 3 therefore run before the caller's memberbase write, through
// preflightCensusGrowth; this function performs 5 and 6.
//
// Censuses backing nothing but drafts are propagated to as well: the census must track the
// memberbase regardless, and it is only the on-chain resize that is limited to published questions.
// A draft published without being edited again would otherwise go on chain missing the member.
func (a *API) propagateMembersToCensuses(
	orgAddress common.Address, censusIDs, memberIDs []string,
) censusPropagation {
	var out censusPropagation
	if len(censusIDs) == 0 || len(memberIDs) == 0 {
		return out
	}

	var targets []censusSizeTarget
	for _, censusID := range censusIDs {
		// idempotent: skips members already present, and recounts and persists the census size
		_, memberErrs, err := a.db.AddCensusParticipantsByMemberIDs(censusID, memberIDs)
		if err != nil {
			log.Warnw("could not add members to census", "census", censusID, "error", err)
			out.Errors = append(out.Errors, fmt.Sprintf("census %s: %v", censusID, err))
			continue
		}
		out.Errors = append(out.Errors, memberErrs...)

		census, err := a.db.Census(censusID)
		if err != nil {
			log.Warnw("could not reload census after adding members", "census", censusID, "error", err)
			continue
		}
		questions, err := a.db.OngoingQuestionsByCensuses([]string{censusID})
		if err != nil {
			log.Warnw("could not resolve the questions of a census", "census", censusID, "error", err)
			continue
		}
		for i := range questions {
			// a question restricted to a named subset is unaffected by who else joined the census
			if len(questions[i].EligibleMemberIDs) > 0 {
				continue
			}
			targets = append(targets, censusSizeTarget{
				question: questions[i], census: census, size: uint64(census.Size),
			})
		}
	}

	jobID, err := a.enqueueSetProcessCensus(orgAddress, targets)
	if err != nil {
		log.Warnw("could not enqueue the census resize after adding members",
			"org", orgAddress, "questions", len(targets), "error", err)
		out.Errors = append(out.Errors, err.Error())
		return out
	}
	if jobID != "" {
		out.JobIDs = append(out.JobIDs, jobID)
	}
	return out
}

// preflightCensusGrowth runs the read-only checks that must pass before the memberbase write:
// the plan's census quota for each target census, and restoring the organization's chain signer.
// Both are refusals that have to leave the memberbase and the censuses untouched, so an over-quota
// request changes nothing at all.
//
// The signer is not returned: enqueueSetProcessCensus restores its own. This only proves it can be.
func (a *API) preflightCensusGrowth(org *db.Organization, censusIDs []string, count int) error {
	if len(censusIDs) == 0 || count == 0 {
		return nil
	}
	for _, censusID := range censusIDs {
		if err := a.subscriptions.OrgCanAddCensusParticipants(org.Address, censusID, count); err != nil {
			return err
		}
	}
	if _, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce); err != nil {
		return fmt.Errorf("could not restore organization signer: %w", err)
	}
	return nil
}

// autoGroupCensuses returns the censuses backed by the organization's auto "All members" group,
// which is the group a newly created member implicitly joins. An organization without one (no
// members yet, or none of its censuses is group-backed) has nothing to propagate to.
func (a *API) autoGroupCensuses(orgAddress common.Address) []string {
	group, err := a.db.AutoMemberGroup(orgAddress)
	if err != nil {
		if err != db.ErrNotFound {
			log.Warnw("could not resolve the auto member group", "org", orgAddress, "error", err)
		}
		return nil
	}
	return group.CensusIDs
}
