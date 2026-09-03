package db

import (
	"context"
	"fmt"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// censusObjectIDs converts census ids from their hex form to the ObjectID form votingProcesses
// stores them in. Note the two representations coexist on purpose: censusParticipants keys the
// census by its hex string, votingProcesses by the ObjectID itself.
func censusObjectIDs(censusIDs []string) ([]bson.ObjectID, error) {
	oids := make([]bson.ObjectID, 0, len(censusIDs))
	for _, id := range censusIDs {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("invalid census id %q: %w", id, ErrInvalidData)
		}
		oids = append(oids, oid)
	}
	return oids, nil
}

// normalizeCSPUserIDs maps member ids onto the form the CSP collections store them in. A CSP
// userid is an internal.HexBytes, which marshals to its lowercase hex string, so a member id that
// differs only in case or in a "0x" prefix would silently miss. Ids that are not hex at all are
// dropped rather than fatal: they cannot have a CSP token, and these ids reach us from request
// bodies, where internal.HexBytesFromString would panic by design.
func normalizeCSPUserIDs(memberIDs []string) []string {
	userIDs := make([]string, 0, len(memberIDs))
	for _, id := range memberIDs {
		hb := internal.HexBytes{}
		if err := hb.ParseString(id); err != nil {
			continue
		}
		userIDs = append(userIDs, hb.String())
	}
	return userIDs
}

// CensusesForMembers returns the ids of every census the given members participate in. Deleting a
// member has to reach all of them, not only the ones reachable through a group.
func (ms *MongoStorage) CensusesForMembers(memberIDs []string) ([]string, error) {
	if len(memberIDs) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	var values []any
	if err := ms.censusParticipants.Distinct(ctx, "censusId",
		bson.M{"participantID": bson.M{"$in": memberIDs}}).Decode(&values); err != nil {
		return nil, fmt.Errorf("failed to query censuses of members: %w", err)
	}

	censusIDs := make([]string, 0, len(values))
	for _, v := range values {
		id, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected censusId type %T in census participants", v)
		}
		censusIDs = append(censusIDs, id)
	}
	return censusIDs, nil
}

// VotingProcessesByCensus returns every voting process built on any of the given censuses.
// A group census can back several processes, so this is the link from a memberbase change back
// to the elections it has to reach.
func (ms *MongoStorage) VotingProcessesByCensus(censusIDs []string) ([]VotingProcess, error) {
	if len(censusIDs) == 0 {
		return nil, nil
	}
	oids, err := censusObjectIDs(censusIDs)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	cursor, err := ms.votingProcesses.Find(ctx, bson.M{"censusId": bson.M{"$in": oids}})
	if err != nil {
		return nil, fmt.Errorf("failed to query voting processes by census: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var processes []VotingProcess
	if err := cursor.All(ctx, &processes); err != nil {
		return nil, fmt.Errorf("failed to decode voting processes: %w", err)
	}
	return processes, nil
}

// OngoingQuestionsByCensuses returns the published questions of those censuses' processes whose
// election still accepts votes — status READY or PAUSED. ENDED, CANCELED and RESULTS are terminal:
// nothing can be signed for them any more, so they release the members they hold.
func (ms *MongoStorage) OngoingQuestionsByCensuses(censusIDs []string) ([]VotingProcessQuestion, error) {
	processes, err := ms.VotingProcessesByCensus(censusIDs)
	if err != nil {
		return nil, err
	}
	if len(processes) == 0 {
		return nil, nil
	}

	processIDs := make([]bson.ObjectID, 0, len(processes))
	for _, p := range processes {
		processIDs = append(processIDs, p.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"processId":  bson.M{"$in": processIDs},
		"upstreamId": bson.M{"$exists": true},
		"status":     bson.M{"$in": []string{QuestionStatusReady, QuestionStatusPaused}},
	}
	cursor, err := ms.processesQuestions.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query ongoing questions by census: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var questions []VotingProcessQuestion
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, fmt.Errorf("failed to decode ongoing questions: %w", err)
	}
	return questions, nil
}

// MembersWithUsedCSPProcesses returns the subset of memberIDs the CSP has already signed for on any
// of the given elections, in one query.
//
// "Signed for" is not "voted": the CSP records consumption when it issues the signature, which is
// before — and regardless of whether — the ballot reaches the chain. That is the conservative
// direction for a guard, but it means the answer must never be reported to a user as "already
// voted".
//
// MembersWithUsedCSPProcess answers the same question one round trip per id and is kept for the
// participants endpoint.
func (ms *MongoStorage) MembersWithUsedCSPProcesses(
	processIDs []internal.HexBytes,
	memberIDs []string,
) ([]string, error) {
	if len(processIDs) == 0 || len(memberIDs) == 0 {
		return nil, nil
	}
	userIDs := normalizeCSPUserIDs(memberIDs)
	if len(userIDs) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// covered by the userid+processid index on cspTokensStatus
	filter := bson.M{
		"processid": bson.M{"$in": processIDs},
		"userid":    bson.M{"$in": userIDs},
		"consumed":  true,
	}
	var values []any
	if err := ms.cspTokensStatus.Distinct(ctx, "userid", filter).Decode(&values); err != nil {
		return nil, fmt.Errorf("failed to query consumed CSP processes: %w", err)
	}

	signed := make(map[string]bool, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected userid type %T in consumed CSP processes", v)
		}
		signed[s] = true
	}

	// answer in the caller's own spelling of the ids, and in the order they asked
	result := make([]string, 0, len(signed))
	for _, id := range memberIDs {
		hb := internal.HexBytes{}
		if err := hb.ParseString(id); err != nil {
			continue
		}
		if signed[hb.String()] {
			result = append(result, id)
		}
	}
	return result, nil
}

// SignedVotersForElections returns every member the CSP has signed for on any of the given
// elections, in one query.
//
// Its sibling MembersWithUsedCSPProcesses answers the same question for a known handful of members.
// This one is for "who has been signed for at all", which is what a question open to the whole
// census needs: it names nobody, so there is no stored list to diff a restriction against.
//
// The ids come back in the CSP's own spelling — the lowercase hex of the member ObjectID — since
// there is no caller list to echo.
func (ms *MongoStorage) SignedVotersForElections(processIDs []internal.HexBytes) ([]string, error) {
	if len(processIDs) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"processid": bson.M{"$in": processIDs}, "consumed": true}
	var values []any
	if err := ms.cspTokensStatus.Distinct(ctx, "userid", filter).Decode(&values); err != nil {
		return nil, fmt.Errorf("failed to query the signed voters of the elections: %w", err)
	}

	voters := make([]string, 0, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected userid type %T in consumed CSP processes", v)
		}
		voters = append(voters, s)
	}
	return voters, nil
}

// RevokeMembersFromCensuses removes members from the given censuses and from every question
// eligibility list built on them, so a memberbase change takes effect on elections already running.
//
// It returns the number of participant rows actually removed — (census, member) pairs, so a member
// in three of the given censuses counts three times — and the published questions whose eligibility
// list became empty. An empty list means "the whole census" (see
// VotingProcessQuestion.EligibleMemberIDs), so such a question just went from a subset to the full
// census while its election was sized on chain for the subset: the caller must enqueue a
// maxCensusSize resize for it.
//
// Callers must refuse the removal first for any member the CSP has already signed for
// (MembersWithUsedCSPProcesses) — this function is the write, not the guard.
func (ms *MongoStorage) RevokeMembersFromCensuses(
	censusIDs, memberIDs []string,
) (int64, []VotingProcessQuestion, error) {
	if len(censusIDs) == 0 || len(memberIDs) == 0 {
		return 0, nil, nil
	}

	processes, err := ms.VotingProcessesByCensus(censusIDs)
	if err != nil {
		return 0, nil, err
	}
	processIDs := make([]bson.ObjectID, 0, len(processes))
	for _, p := range processes {
		processIDs = append(processIDs, p.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// The published questions that currently name one of these members. Whichever of them has no
	// eligible member left once these are removed has silently become whole-census and needs its
	// election resized. The emptiness is decided here, before the write, from each candidate's own
	// eligibility list: a failure after the commit cannot lose the resize targets, and a retry that
	// finds no candidates (the $pull already pruned them) is not needed to recompute them.
	candidates, err := ms.publishedQuestionsNaming(ctx, processIDs, memberIDs)
	if err != nil {
		return 0, nil, err
	}
	revoked := make(map[string]struct{}, len(memberIDs))
	for _, m := range memberIDs {
		revoked[m] = struct{}{}
	}
	emptied := make([]VotingProcessQuestion, 0, len(candidates))
	for _, q := range candidates {
		if q.emptiedByRevocation(revoked) {
			emptied = append(emptied, q)
		}
	}

	removed, err := ms.revokeWrites(ctx, censusIDs, memberIDs, processIDs)
	if err != nil {
		// emptied was decided before the write, so it is returned even on a partial-commit
		// failure: the questions it names are emptied by whatever portion of the $pull committed,
		// and the caller still needs to enqueue their resize.
		return removed, emptied, err
	}

	// updateCensusSize takes keysLock through SetCensus, which is not reentrant, so it runs
	// outside the write section above. A recount failure is logged rather than returned: the
	// revocation has committed, and a stale size after a removal errs high — the resize path
	// never shrinks and reads the chain first, so an over-sized target is harmless while a
	// misreported failure would hide the committed removal and the emptied questions.
	for _, censusID := range censusIDs {
		if err := ms.updateCensusSize(censusID); err != nil {
			log.Warnw("failed to recount census after revocation", "census", censusID, "error", err)
		}
	}

	return removed, emptied, nil
}

// RevokeMembersEverywhere revokes the given members from every census they participate in. This is
// the member-deletion form of RevokeMembersFromCensuses, where the censuses are not known up front.
func (ms *MongoStorage) RevokeMembersEverywhere(memberIDs []string) (int64, []VotingProcessQuestion, error) {
	censusIDs, err := ms.CensusesForMembers(memberIDs)
	if err != nil {
		return 0, nil, err
	}
	return ms.RevokeMembersFromCensuses(censusIDs, memberIDs)
}

// publishedQuestionsNaming returns the published questions whose eligibility list contains any of
// the given members — the only questions a revocation can empty. A whole-census question holds no
// named members (its eligibleMemberIds is nil, empty or missing), so the eligibleMemberIds $in
// filter excludes it; that is why emptiedByRevocation never has to reason about the
// null/missing/empty-array equivalence the write-side $pull has to guard against (see revokeWrites).
func (ms *MongoStorage) publishedQuestionsNaming(
	ctx context.Context,
	processIDs []bson.ObjectID,
	memberIDs []string,
) ([]VotingProcessQuestion, error) {
	if len(processIDs) == 0 {
		return nil, nil
	}
	filter := bson.M{
		"processId":         bson.M{"$in": processIDs},
		"upstreamId":        bson.M{"$exists": true},
		"eligibleMemberIds": bson.M{"$in": memberIDs},
	}
	cursor, err := ms.processesQuestions.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to query questions naming the revoked members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var questions []VotingProcessQuestion
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, fmt.Errorf("failed to decode questions naming the revoked members: %w", err)
	}
	return questions, nil
}

// emptiedByRevocation reports whether removing the given members leaves the question with no
// eligible member — i.e. every member it names is being revoked. A candidate (returned by
// publishedQuestionsNaming) always names at least one revoked member, so a zero-length list never
// reaches the loop; the guard keeps the method honest on its own.
func (q VotingProcessQuestion) emptiedByRevocation(revoked map[string]struct{}) bool {
	if len(q.EligibleMemberIDs) == 0 {
		return false
	}
	for _, id := range q.EligibleMemberIDs {
		if _, ok := revoked[id]; !ok {
			return false
		}
	}
	return true
}

// revokeWrites performs the three deletions the revocation consists of, under the write lock, and
// reports how many participant rows step 1 removed.
//
// The three are sequential, not transactional — keysLock is a process-local mutex and the repo opens
// no mongo session anywhere. A failure part-way leaves the earlier steps committed. That is
// deliberate rather than merely tolerated: step 1 is the one that revokes, since the CSP re-checks
// participation at sign time, so it goes first and a partial failure leaves a member who cannot
// vote either way. What survives is cosmetic — a stale entry in an eligibility list naming someone
// no longer in the census, and a CSP session that fails its own participation re-check on first use.
// Re-running the revocation is idempotent and clears both.
//
// The caller must have scoped memberIDs to an organization already; see FilterOrgMemberIDs. This
// derives the censuses from the members, which is what makes it usable from the member, group and
// erasure paths, and is also why it cannot do the scoping itself.
func (ms *MongoStorage) revokeWrites(
	ctx context.Context,
	censusIDs, memberIDs []string,
	processIDs []bson.ObjectID,
) (int64, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// 1. the census participant rows. This is what actually revokes: the CSP re-checks
	//    participation at sign time, and the census size is a count of these. Its DeletedCount is
	//    the only honest answer to "how many members were removed" — the ids come from a request
	//    body, so some of them routinely name nobody.
	res, err := ms.censusParticipants.DeleteMany(ctx, bson.M{
		"censusId":      bson.M{"$in": censusIDs},
		"participantID": bson.M{"$in": memberIDs},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to delete census participants: %w", err)
	}

	// 2. the eligibility lists, for drafts as well as published questions — the memberbase is the
	//    source of truth for both. The $type guard is required, not an optimization: a
	//    whole-census question holds a nil slice, which marshals to null rather than [], and
	//    $pull fails the entire batch on a non-array value.
	if len(processIDs) > 0 {
		if _, err := ms.processesQuestions.UpdateMany(
			ctx,
			bson.M{
				"processId":         bson.M{"$in": processIDs},
				"eligibleMemberIds": bson.M{"$type": "array"},
			},
			bson.M{"$pull": bson.M{"eligibleMemberIds": bson.M{"$in": memberIDs}}},
		); err != nil {
			return 0, fmt.Errorf("failed to prune question eligibility lists: %w", err)
		}
	}

	// 3. the CSP auth sessions. Re-authentication re-checks census participation, so dropping a
	//    session the member may still hold for another election only forces a fresh login.
	if userIDs := normalizeCSPUserIDs(memberIDs); len(userIDs) > 0 {
		if _, err := ms.cspTokens.DeleteMany(ctx, bson.M{"userid": bson.M{"$in": userIDs}}); err != nil {
			return 0, fmt.Errorf("failed to delete CSP auth sessions: %w", err)
		}
	}

	// 4. cspTokensStatus is deliberately left alone. ConsumeCSPProcess pins the single address a
	//    member was ever signed for; deleting that row would let a removed-then-re-added member be
	//    signed for a second address, producing a second nullifier and a double vote the chain
	//    accepts.

	return res.DeletedCount, nil
}
