package db

import (
	"context"
	"fmt"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// censusObjectIDs converts census ids from their hex form to the ObjectID form votingProcesses
// stores them in. Note the two representations coexist on purpose: censusParticipants keys the
// census by its hex string, votingProcesses by the ObjectID itself.
func censusObjectIDs(censusIDs []string) ([]primitive.ObjectID, error) {
	oids := make([]primitive.ObjectID, 0, len(censusIDs))
	for _, id := range censusIDs {
		oid, err := primitive.ObjectIDFromHex(id)
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

	values, err := ms.censusParticipants.Distinct(ctx, "censusId",
		bson.M{"participantID": bson.M{"$in": memberIDs}})
	if err != nil {
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

	processIDs := make([]primitive.ObjectID, 0, len(processes))
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
	values, err := ms.cspTokensStatus.Distinct(ctx, "userid", filter)
	if err != nil {
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

// RevokeMembersFromCensuses removes members from the given censuses and from every question
// eligibility list built on them, so a memberbase change takes effect on elections already running.
//
// It returns the published questions whose eligibility list became empty. An empty list means "the
// whole census" (see VotingProcessQuestion.EligibleMemberIDs), so such a question just went from a
// subset to the full census while its election was sized on chain for the subset: the caller must
// enqueue a maxCensusSize resize for it.
//
// Callers must refuse the removal first for any member the CSP has already signed for
// (MembersWithUsedCSPProcesses) — this function is the write, not the guard.
func (ms *MongoStorage) RevokeMembersFromCensuses(
	censusIDs, memberIDs []string,
) ([]VotingProcessQuestion, error) {
	if len(censusIDs) == 0 || len(memberIDs) == 0 {
		return nil, nil
	}

	processes, err := ms.VotingProcessesByCensus(censusIDs)
	if err != nil {
		return nil, err
	}
	processIDs := make([]primitive.ObjectID, 0, len(processes))
	for _, p := range processes {
		processIDs = append(processIDs, p.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	// The published questions that currently name one of these members: whichever of them ends up
	// with an empty list has silently become whole-census and needs its election resized.
	candidates, err := ms.publishedQuestionsNaming(ctx, processIDs, memberIDs)
	if err != nil {
		return nil, err
	}

	if err := ms.revokeWrites(ctx, censusIDs, memberIDs, processIDs); err != nil {
		return nil, err
	}

	// updateCensusSize takes keysLock through SetCensus, which is not reentrant, so it runs
	// outside the write section above.
	for _, censusID := range censusIDs {
		if err := ms.updateCensusSize(censusID); err != nil {
			return nil, fmt.Errorf("failed to recount census %s: %w", censusID, err)
		}
	}

	return ms.emptiedQuestions(ctx, candidates)
}

// RevokeMembersEverywhere revokes the given members from every census they participate in. This is
// the member-deletion form of RevokeMembersFromCensuses, where the censuses are not known up front.
func (ms *MongoStorage) RevokeMembersEverywhere(memberIDs []string) ([]VotingProcessQuestion, error) {
	censusIDs, err := ms.CensusesForMembers(memberIDs)
	if err != nil {
		return nil, err
	}
	return ms.RevokeMembersFromCensuses(censusIDs, memberIDs)
}

// publishedQuestionsNaming returns the ids of published questions whose eligibility list contains
// any of the given members.
func (ms *MongoStorage) publishedQuestionsNaming(
	ctx context.Context,
	processIDs []primitive.ObjectID,
	memberIDs []string,
) ([]primitive.ObjectID, error) {
	if len(processIDs) == 0 {
		return nil, nil
	}
	filter := bson.M{
		"processId":         bson.M{"$in": processIDs},
		"upstreamId":        bson.M{"$exists": true},
		"eligibleMemberIds": bson.M{"$in": memberIDs},
	}
	cursor, err := ms.processesQuestions.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
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
	ids := make([]primitive.ObjectID, 0, len(questions))
	for _, q := range questions {
		ids = append(ids, q.ID)
	}
	return ids, nil
}

// revokeWrites performs the three deletions the revocation consists of, under the write lock.
func (ms *MongoStorage) revokeWrites(
	ctx context.Context,
	censusIDs, memberIDs []string,
	processIDs []primitive.ObjectID,
) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// 1. the census participant rows. This is what actually revokes: the CSP re-checks
	//    participation at sign time, and the census size is a count of these.
	if _, err := ms.censusParticipants.DeleteMany(ctx, bson.M{
		"censusId":      bson.M{"$in": censusIDs},
		"participantID": bson.M{"$in": memberIDs},
	}); err != nil {
		return fmt.Errorf("failed to delete census participants: %w", err)
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
			return fmt.Errorf("failed to prune question eligibility lists: %w", err)
		}
	}

	// 3. the CSP auth sessions. Re-authentication re-checks census participation, so dropping a
	//    session the member may still hold for another election only forces a fresh login.
	if userIDs := normalizeCSPUserIDs(memberIDs); len(userIDs) > 0 {
		if _, err := ms.cspTokens.DeleteMany(ctx, bson.M{"userid": bson.M{"$in": userIDs}}); err != nil {
			return fmt.Errorf("failed to delete CSP auth sessions: %w", err)
		}
	}

	// 4. cspTokensStatus is deliberately left alone. ConsumeCSPProcess pins the single address a
	//    member was ever signed for; deleting that row would let a removed-then-re-added member be
	//    signed for a second address, producing a second nullifier and a double vote the chain
	//    accepts.

	return nil
}

// emptiedQuestions re-reads the given questions and returns those left with no eligible member.
func (ms *MongoStorage) emptiedQuestions(
	ctx context.Context,
	candidates []primitive.ObjectID,
) ([]VotingProcessQuestion, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	cursor, err := ms.processesQuestions.Find(ctx, bson.M{
		"_id":               bson.M{"$in": candidates},
		"eligibleMemberIds": bson.M{"$in": bson.A{nil, bson.A{}}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query emptied questions: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var questions []VotingProcessQuestion
	if err := cursor.All(ctx, &questions); err != nil {
		return nil, fmt.Errorf("failed to decode emptied questions: %w", err)
	}
	return questions, nil
}
