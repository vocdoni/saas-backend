package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// censusBuildError maps a failed census build to the API error to return. Members that cannot be
// told apart are a 400 naming them, in the shape the census pre-flight reports them, and so is a bad
// member or group ID: those are the caller's to fix, not a 500.
func censusBuildError(err error) error {
	var clash *db.CensusMembersError
	switch {
	case errors.As(err, &clash):
		return errors.ErrInvalidData.
			Withf("members share login data with another member: fix their data or remove them").
			WithData(clash)
	case errors.Is(err, db.ErrInvalidData):
		return errors.ErrInvalidData.WithErr(err)
	default:
		return fmt.Errorf("failed to populate census: %w", err)
	}
}

// anonymousVotingUnavailable is the message returned for an anonymous census while anonymous
// voting is temporarily off (csp.Config.DisableBlindCSP).
const anonymousVotingUnavailable = csp.AnonymousVotingUnavailable

// anonymousCensusRefused reports whether an anonymous census must be refused because anonymous
// voting is temporarily off.
func (a *API) anonymousCensusRefused(anonymous bool) bool {
	return anonymous && a.csp.BlindCSPDisabled()
}

// attestedCensusProblem returns why an attested census spec cannot be used, or "" when the spec is
// valid or not attested. An attested census has no member list, so every member-based option is
// refused, and its 2FA is the one channel the integrator attests a contact for.
func attestedCensusProblem(spec apicommon.CensusSpec, questions []apicommon.VotingProcessQuestionRequest) string {
	att := spec.Attested
	if att == nil {
		return ""
	}
	switch {
	case att.Signer == (common.Address{}):
		return "attested census needs a signer"
	case att.MaxCensusSize <= 0 || att.MaxCensusSize > pricing.MaxCensusSize:
		return fmt.Sprintf("attested census maxCensusSize must be between 1 and %d", pricing.MaxCensusSize)
	case spec.GroupID != "" || len(spec.MemberIDs) > 0:
		return "attested census cannot have groupId or memberIds"
	case len(spec.AuthFields) > 0:
		return "attested census cannot have authFields"
	case spec.Weighted:
		return "attested census cannot be weighted"
	case spec.Anonymous:
		return "attested census cannot be anonymous"
	case !attestedTwoFaFields(spec.TwoFaFields):
		return `attested census twoFaFields must be [], ["email"] or ["phone"]`
	}
	for i := range questions {
		if questions[i].Eligibility != nil {
			return fmt.Sprintf("question %d: attested census cannot restrict eligibility", i)
		}
	}
	return ""
}

// attestedCensusPublishProblems re-checks a stored attested census at publish time, so a census that
// reached the database without the create and update checks cannot publish.
func attestedCensusPublishProblems(census *db.Census, questions []db.VotingProcessQuestion) []string {
	var problems []string
	if census.Attested.Signer == (common.Address{}) {
		problems = append(problems, "attested census has no signer")
	}
	if census.Size <= 0 {
		problems = append(problems, "attested census has no maxCensusSize")
	}
	if census.Weighted || census.Anonymous || len(census.AuthFields) > 0 || !census.GroupID.IsZero() ||
		!attestedTwoFaFields(census.TwoFaFields) {
		problems = append(problems, "attested census combines member-based options")
	}
	for i := range questions {
		if len(questions[i].EligibleMemberIDs) > 0 {
			problems = append(problems, fmt.Sprintf("question %d: attested census cannot restrict eligibility", i))
		}
	}
	return problems
}

// attestedTwoFaFields reports whether fields is a 2FA setting an attested census allows: none, or
// exactly one channel.
func attestedTwoFaFields(fields db.OrgMemberTwoFaFields) bool {
	return len(fields) == 0 || (len(fields) == 1 &&
		(fields[0] == db.OrgMemberTwoFaFieldEmail || fields[0] == db.OrgMemberTwoFaFieldPhone))
}

// echoAttestationDomain adds the verifying contract of the attestation domain to the census echo of
// an attested process, so the integrator signs for the environment it talks to.
func (a *API) echoAttestationDomain(spec *apicommon.CensusSpec) {
	if spec.Attested == nil || a.csp == nil {
		return
	}
	domain, err := a.csp.AttestationDomain()
	if err != nil {
		return
	}
	spec.Attested.VerifyingContract = &domain.VerifyingContract
}

// resolveOrCreateDefaultCensus materializes the inline census spec of a voting process into
// a db.Census (auth/2FA policy + participants) and returns it. The census type is inferred
// from the 2FA fields (SetCensus does this). The census is created unpublished; publishing happens at
// process publish time. Members missing the auth data the census requires are left out of it and
// returned, as hex IDs, for the caller to report.
func (a *API) resolveOrCreateDefaultCensus(
	spec apicommon.CensusSpec, orgAddress common.Address,
) (*db.Census, []string, error) {
	if a.anonymousCensusRefused(spec.Anonymous) {
		return nil, nil, errors.ErrInvalidData.With(anonymousVotingUnavailable)
	}
	census := &db.Census{
		OrgAddress:  orgAddress,
		Weighted:    spec.Weighted,
		Anonymous:   spec.Anonymous,
		AuthFields:  spec.AuthFields,
		TwoFaFields: spec.TwoFaFields,
		CreatedAt:   time.Now(),
	}
	if spec.Attested != nil {
		// an attested census has no members: its declared maximum sizes and prices the process
		census.Size = spec.Attested.MaxCensusSize
		census.Attested = &db.AttestedCensus{
			Signer:        spec.Attested.Signer,
			SignerHistory: []db.AttestationSigner{{Signer: spec.Attested.Signer, From: census.CreatedAt}},
		}
	}
	censusID, err := a.db.SetCensus(census)
	if err != nil {
		// a census the caller configured wrong is theirs to fix, not a 500
		if errors.Is(err, db.ErrPasswordNeedsLookupField) {
			return nil, nil, errors.ErrInvalidData.With(db.PasswordNeedsLookupFieldMessage)
		}
		return nil, nil, fmt.Errorf("failed to create census: %w", err)
	}
	// self-clean: if any step below fails, delete the census we just created so a failed
	// resolve leaves nothing behind (DelCensus also removes any participants added).
	committed := false
	defer func() {
		if !committed {
			_ = a.db.DelCensus(censusID)
		}
	}()

	var missing []bson.ObjectID
	switch {
	case spec.GroupID != "":
		if _, missing, err = a.db.PopulateGroupCensus(census, spec.GroupID); err != nil {
			return nil, nil, censusBuildError(err)
		}
	case len(spec.MemberIDs) > 0:
		if _, missing, err = a.db.PopulateMembersCensus(census, spec.MemberIDs); err != nil {
			return nil, nil, censusBuildError(err)
		}
	case spec.Attested != nil:
		committed = true
		return census, nil, nil
	default:
		// no members and no group: an empty (auth-only shell) census
	}

	// persist the resulting census size for downstream maxCensusSize computation
	size, err := a.db.CountCensusParticipants(censusID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to count census participants: %w", err)
	}
	census.Size = size
	if _, err := a.db.SetCensus(census); err != nil {
		return nil, nil, fmt.Errorf("failed to update census size: %w", err)
	}
	committed = true
	missingIDs := make([]string, len(missing))
	for i, id := range missing {
		missingIDs[i] = id.Hex()
	}
	return census, missingIDs, nil
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
	// every id must be a participant of the census (subset ⊆ default invariant)
	return a.validateCensusParticipants(census.ID.Hex(), memberIDs)
}

// validateCensusParticipants returns memberIDs deduplicated and in input order, having checked in a
// single query that every one of them is a participant of the census.
//
// The contract of the eligibility endpoints is "resend the whole list", so a per-id FindOne would
// cost one round trip per member on every call. A Mongo failure is returned as a plain error so it
// surfaces as a 500, never as "member X is not part of the census".
func (a *API) validateCensusParticipants(censusID string, memberIDs []string) ([]string, error) {
	ordered := make([]string, 0, len(memberIDs))
	seen := make(map[string]bool, len(memberIDs))
	for _, id := range memberIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	if len(ordered) == 0 {
		return nil, nil
	}

	participants, err := a.db.CensusParticipantsByMemberIDs(censusID, ordered)
	if err != nil {
		return nil, fmt.Errorf("could not read census participants: %w", err)
	}
	present := make(map[string]bool, len(participants))
	for i := range participants {
		present[participants[i].ParticipantID] = true
	}
	var missing []string
	for _, id := range ordered {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, errors.ErrInvalidData.Withf(
			"members are not part of the process census: %s", strings.Join(missing, ", "),
		)
	}
	return ordered, nil
}
