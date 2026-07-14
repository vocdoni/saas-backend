package api

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// resolveOrCreateDefaultCensus materializes the inline census spec of a voting process into
// a db.Census (auth/2FA policy + participants) and returns it. The census type is inferred
// from the 2FA fields (SetCensus does this). Census/vote quotas are enforced, mirroring
// addCensusParticipantsHandler. The census is created unpublished; publishing happens at
// process publish time.
func (a *API) resolveOrCreateDefaultCensus(spec apicommon.CensusSpec, orgAddress common.Address) (*db.Census, error) {
	census := &db.Census{
		OrgAddress:  orgAddress,
		Weighted:    spec.Weighted,
		AuthFields:  spec.AuthFields,
		TwoFaFields: spec.TwoFaFields,
		CreatedAt:   time.Now(),
	}
	censusID, err := a.db.SetCensus(census)
	if err != nil {
		return nil, fmt.Errorf("failed to create census: %w", err)
	}
	// self-clean: if any step below fails, delete the census we just created so a failed
	// resolve leaves nothing behind (DelCensus also removes any participants added).
	committed := false
	defer func() {
		if !committed {
			_ = a.db.DelCensus(censusID)
		}
	}()

	switch {
	case spec.GroupID != "":
		if _, err := a.db.PopulateGroupCensus(census, spec.GroupID); err != nil {
			return nil, fmt.Errorf("failed to populate group census: %w", err)
		}
		if err := a.subscriptions.OrgCanAddCensusParticipants(orgAddress, censusID, 0); err != nil {
			return nil, err
		}
	case len(spec.MemberIDs) > 0:
		if err := a.subscriptions.OrgCanAddCensusParticipants(orgAddress, censusID, len(spec.MemberIDs)); err != nil {
			return nil, err
		}
		if _, _, err := a.db.AddCensusParticipantsByMemberIDs(censusID, spec.MemberIDs); err != nil {
			return nil, fmt.Errorf("failed to add census participants: %w", err)
		}
	default:
		// no members and no group: an empty (auth-only shell) census
	}

	// persist the resulting census size for downstream maxCensusSize computation
	size, err := a.db.CountCensusParticipants(censusID)
	if err != nil {
		return nil, fmt.Errorf("failed to count census participants: %w", err)
	}
	census.Size = size
	if _, err := a.db.SetCensus(census); err != nil {
		return nil, fmt.Errorf("failed to update census size: %w", err)
	}
	committed = true
	return census, nil
}

// resolveEligibleMemberIDs resolves a question's optional eligibility subset to a list of
// member ids, validating that each is a member of the census (the subset ⊆ census invariant
// is enforced here, at insert time). A nil/empty spec, or an auto "All members" group,
// means every census member is eligible and returns nil.
func (a *API) resolveEligibleMemberIDs(
	elig *apicommon.EligibilitySpec, census *db.Census, orgAddress common.Address,
) ([]string, error) {
	if elig == nil {
		return nil, nil
	}
	memberIDs := elig.MemberIDs
	if elig.GroupID != "" {
		group, err := a.db.OrganizationMemberGroup(elig.GroupID, orgAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to get group: %w", err)
		}
		// an auto group covers all members ⇒ no subset restriction
		if group.IsAutoGroup {
			return nil, nil
		}
		memberIDs = append(memberIDs, group.MemberIDs...)
	}
	if len(memberIDs) == 0 {
		return nil, nil
	}
	// validate each id is a participant of the census (subset ⊆ default invariant)
	out := make([]string, 0, len(memberIDs))
	seen := make(map[string]bool, len(memberIDs))
	for _, id := range memberIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := a.db.CensusParticipant(census.ID.Hex(), id); err != nil {
			return nil, errors.ErrInvalidData.Withf("member %s is not part of the process census", id)
		}
		out = append(out, id)
	}
	return out, nil
}
