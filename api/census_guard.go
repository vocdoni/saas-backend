package api

import (
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// blockedVoters returns the subset of memberIDs that may not be removed from the given censuses
// because the CSP has already signed for them in a process that is still running.
//
// "Still running" means a published question whose status is READY or PAUSED. ENDED, CANCELED and
// RESULTS release their members, so once voting closes the removal goes through and erasure is
// never permanently blocked.
//
// Refusing rather than silently revoking is what keeps census.Size an honest recount of live
// participants, and votes <= censusSize true by construction: no signature the chain will accept can
// belong to someone who is no longer counted.
func (a *API) blockedVoters(censusIDs, memberIDs []string) ([]string, error) {
	if len(censusIDs) == 0 || len(memberIDs) == 0 {
		return nil, nil
	}
	questions, err := a.db.OngoingQuestionsByCensuses(censusIDs)
	if err != nil {
		return nil, fmt.Errorf("could not resolve the ongoing questions of the censuses: %w", err)
	}
	if len(questions) == 0 {
		return nil, nil
	}
	elections := make([]internal.HexBytes, 0, len(questions))
	for i := range questions {
		elections = append(elections, questions[i].UpstreamID)
	}
	return a.db.MembersWithUsedCSPProcesses(elections, memberIDs)
}

// refuseBlockedVoters answers 409 when any of the members may not be removed, and reports whether
// it did.
//
// It must be called before any write. Refusing after the memberbase has been updated would leave
// the member out of their group but still in the census — exactly the disagreement between the two
// that this cascade exists to remove — while reporting to the caller that nothing happened.
//
// The check and the write it guards are not atomic with respect to the CSP: ProcessSignHandler
// (csp/handlers/processes.go) can issue a signature between this read and the participant delete in
// RevokeMembersFromCensuses, leaving a member removed yet signed for. The window is one request
// round trip, and the sign handler's own participation re-check narrows it but cannot close it —
// that would need a lock shared by the api and csp paths, which a single stale signature (itself
// bounded by the consumption row cspTokensStatus keeps) does not justify.
func (a *API) refuseBlockedVoters(w http.ResponseWriter, censusIDs, memberIDs []string) bool {
	blocked, err := a.blockedVoters(censusIDs, memberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if len(blocked) == 0 {
		return false
	}
	errors.ErrCensusMemberAlreadySignedFor.WithData(map[string]any{"signedMemberIds": blocked}).Write(w)
	return true
}

// refuseVotersLosingEligibility answers 409 when restricting a question to allowed would take the
// vote away from a member the CSP has already signed for, and reports whether it did.
//
// It asks who has been signed for rather than diffing against the stored list, because the diff
// cannot see the case that matters most: a question open to the whole census names nobody, so
// nobody appears as removed even though every member outside allowed is losing the right to vote.
//
// Unlike refuseBlockedVoters this is scoped to the question's own election — losing eligibility for
// one question says nothing about the others sharing the census — and only while that election can
// still be voted. A draft has no election, and a terminal one releases its voters, matching what
// OngoingQuestionsByCensuses treats as ongoing elsewhere.
func (a *API) refuseVotersLosingEligibility(
	w http.ResponseWriter, question *db.VotingProcessQuestion, allowed []string,
) bool {
	// an empty list is "no restriction": it opens the question up, and takes eligibility from nobody
	if len(allowed) == 0 || len(question.UpstreamID) == 0 {
		return false
	}
	if question.Status != db.QuestionStatusReady && question.Status != db.QuestionStatusPaused {
		return false
	}
	voters, err := a.db.SignedVotersForElections([]internal.HexBytes{question.UpstreamID})
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	stays := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		stays[id] = true
	}
	var blocked []string
	for _, id := range voters {
		if !stays[id] {
			blocked = append(blocked, id)
		}
	}
	if len(blocked) == 0 {
		return false
	}
	errors.ErrCensusMemberAlreadySignedFor.WithData(map[string]any{"signedMemberIds": blocked}).Write(w)
	return true
}

// resizeEmptiedQuestions enqueues a maxCensusSize increase for questions whose eligibility list was
// just pruned to empty. An empty list means the whole census, so each of those elections silently
// went from a named subset to everyone while it was sized on chain for the subset.
//
// Returns the job id — empty when nothing needed resizing — and the failures that kept a question
// from being resized. The revocation this follows has already committed, so a failure cannot fail
// the request; but nothing sweeps a resize that was never enqueued, so an empty job id must not
// read the same as "no resize was needed" — callers report the errors in the response body.
//
// The loop below reads a process and a census per question rather than batching them. Emptying a
// question requires removing every member it named, so the input is the handful of published
// questions a single request stripped bare — group them into one query if that ever stops holding.
func (a *API) resizeEmptiedQuestions(
	orgAddress common.Address, emptied []db.VotingProcessQuestion,
) (string, []string) {
	if len(emptied) == 0 {
		return "", nil
	}
	var errs []string
	targets := make([]censusSizeTarget, 0, len(emptied))
	for i := range emptied {
		vp, err := a.db.VotingProcess(emptied[i].ProcessID)
		if err != nil {
			log.Warnw("could not load the process of an emptied question",
				"question", emptied[i].ID.Hex(), "error", err)
			errs = append(errs, fmt.Sprintf("question %s: %v", emptied[i].ID.Hex(), err))
			continue
		}
		census, err := a.db.Census(vp.CensusID.Hex())
		if err != nil {
			log.Warnw("could not load the census of an emptied question",
				"question", emptied[i].ID.Hex(), "error", err)
			errs = append(errs, fmt.Sprintf("question %s: %v", emptied[i].ID.Hex(), err))
			continue
		}
		targets = append(targets, censusSizeTarget{
			question: emptied[i], census: census, size: uint64(census.Size),
		})
	}
	jobID, err := a.enqueueSetProcessCensus(orgAddress, targets)
	if err != nil {
		log.Warnw("could not enqueue the resize of newly whole-census questions",
			"org", orgAddress, "questions", len(targets), "error", err)
		errs = append(errs, err.Error())
		return "", errs
	}
	return jobID, errs
}
