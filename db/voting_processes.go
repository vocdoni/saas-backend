package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// SetVotingProcess creates or replaces a voting process document. It assigns an ID and
// CreatedAt on first insert and always refreshes UpdatedAt. The referenced questions are
// stored separately (see SetQuestion); this only persists the container and its ordered
// QuestionIDs.
func (ms *MongoStorage) SetVotingProcess(vp *VotingProcess) (bson.ObjectID, error) {
	if (vp.OrgAddress.Cmp(common.Address{}) == 0) {
		return bson.NilObjectID, ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	if _, err := ms.Organization(vp.OrgAddress); err != nil {
		return bson.NilObjectID, fmt.Errorf("failed to get organization %s: %w", vp.OrgAddress, err)
	}

	now := time.Now()
	if vp.ID.IsZero() {
		vp.ID = bson.NewObjectID()
		vp.CreatedAt = now
	}
	vp.UpdatedAt = now

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": vp.ID} //nolint:goconst
	opts := options.Replace().SetUpsert(true)
	if _, err := ms.votingProcesses.ReplaceOne(ctx, filter, vp, opts); err != nil {
		return bson.NilObjectID, fmt.Errorf("failed to create or update voting process: %w", err)
	}
	return vp.ID, nil
}

// SetVotingProcessDraft replaces a voting process only while it is still an unpublished draft that
// no publish worker holds, and — when seen is non-zero — only while its stored UpdatedAt still
// equals it, i.e. nothing has written the document since the caller read it. It reports ErrConflict
// otherwise, leaving the stored document untouched. It enforces the same organization invariant as
// SetVotingProcess but never creates: an unknown id is a conflict, not an insert.
//
// The publish precondition is part of the write rather than a check the caller makes beforehand
// because the caller's read is minutes stale by the time it writes: updateVotingProcessHandler
// resolves a census and validates every question in between, and it clears Publishing on the way
// out. A claim landing inside that window used to be replaced away, letting a second publish claim
// a process the first worker was already putting on chain, with its census repointed and its
// questions rewritten underneath it. A stale marker is still overwritten, which is the intended
// release — ClaimVotingProcessForPublish would reclaim it anyway.
//
// The caller cannot tell the two refusals apart from ErrConflict alone; re-read the process to see
// which precondition failed.
func (ms *MongoStorage) SetVotingProcessDraft(vp *VotingProcess, seen time.Time) error {
	if vp.ID.IsZero() || (vp.OrgAddress.Cmp(common.Address{}) == 0) {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// a voting process outlives its organization (nothing deletes votingProcesses on teardown), so
	// this is checked here exactly as in SetVotingProcess rather than assumed from the caller
	if _, err := ms.Organization(vp.OrgAddress); err != nil {
		return fmt.Errorf("failed to get organization %s: %w", vp.OrgAddress, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": vp.ID, "published": false, "$or": bson.A{
		bson.M{"publishing": bson.M{"$exists": false}},
		bson.M{"publishing": bson.M{"$lt": time.Now().Add(-PublishStaleAfter)}},
	}}
	now := time.Now()
	if !seen.IsZero() {
		// mongo stores datetimes with millisecond precision, so the token the client echoes back
		// can only ever match a truncated value — compare against, and advance past, the same
		// truncation
		seen = seen.UTC().Truncate(time.Millisecond)
		if !now.Truncate(time.Millisecond).After(seen) {
			// two writes inside the same millisecond would leave the token unchanged, and the
			// second client's already-consumed token would still match. Force it forward instead.
			now = seen.Add(time.Millisecond)
		}
		filter["updatedAt"] = seen
	}
	prev := vp.UpdatedAt
	vp.UpdatedAt = now
	res, err := ms.votingProcesses.ReplaceOne(ctx, filter, vp)
	if err != nil {
		vp.UpdatedAt = prev
		return fmt.Errorf("failed to update voting process: %w", err)
	}
	if res.MatchedCount == 0 {
		vp.UpdatedAt = prev
		return ErrConflict
	}
	return nil
}

// SetVotingProcessQuestionIDs records the process's ordered question ids. It touches only that
// field, so it cannot undo a concurrent edit of the rest of the document the way a whole-document
// replace would, and it advances updatedAt without ever moving it backwards: the conditional-update
// token of SetVotingProcessDraft has to stay monotonic, and this write can land inside the same
// millisecond as the one that produced the token a client is holding.
func (ms *MongoStorage) SetVotingProcessQuestionIDs(id bson.ObjectID, questionIDs []bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// $max ignores a missing operand, so a document written before updatedAt existed just gets now
	update := mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"questionIds": questionIDs,
		"updatedAt":   bson.M{"$max": bson.A{"$updatedAt", time.Now()}},
	}}}}
	res, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return fmt.Errorf("failed to set voting process question ids: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// VotingProcess returns a voting process by its hex ObjectID.
func (ms *MongoStorage) VotingProcess(id bson.ObjectID) (*VotingProcess, error) {
	if id == bson.NilObjectID {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	vp := &VotingProcess{}
	if err := ms.votingProcesses.FindOne(ctx, bson.M{"_id": id}).Decode(vp); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get voting process: %w", err)
	}
	return vp, nil
}

// ProcessWithQuestions returns a voting process together with its questions ordered by
// their Order field.
func (ms *MongoStorage) ProcessWithQuestions(id bson.ObjectID) (*VotingProcess, []VotingProcessQuestion, error) {
	vp, err := ms.VotingProcess(id)
	if err != nil {
		return nil, nil, err
	}
	questions, err := ms.QuestionsByProcess(id)
	if err != nil {
		return nil, nil, err
	}
	return vp, questions, nil
}

// DeleteVotingProcess removes a voting process and all of its questions. On-chain
// elections already created are immutable and are not affected.
func (ms *MongoStorage) DeleteVotingProcess(id bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.DeleteMany(ctx, bson.M{"processId": id}); err != nil { //nolint:goconst
		return fmt.Errorf("failed to delete voting process questions: %w", err)
	}
	if _, err := ms.votingProcesses.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		return fmt.Errorf("failed to delete voting process: %w", err)
	}
	return nil
}

// ListVotingProcesses returns a paginated list of voting processes for an organization, filtered by
// draft state (PublishedOnly serves the public/non-manager view; AllProcesses the manager view).
// When questionStatus is non-empty, only processes that have at least one question in that status
// are returned.
func (ms *MongoStorage) ListVotingProcesses(
	orgAddress common.Address, questionStatus string, draft DraftFilter, page, limit int64,
) (int64, []VotingProcess, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	filter := bson.M{"orgAddress": orgAddress} //nolint:goconst
	switch draft {
	case DraftOnly:
		filter["published"] = false
	case PublishedOnly:
		filter["published"] = true
	default:
		// AllProcesses — no published filter
	}
	if questionStatus != "" {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		var ids []any
		if err := ms.processesQuestions.Distinct(ctx, "processId", bson.M{
			"orgAddress": orgAddress,
			"status":     questionStatus, //nolint:goconst
		}).Decode(&ids); err != nil {
			return 0, nil, fmt.Errorf("failed to filter processes by question status: %w", err)
		}
		filter["_id"] = bson.M{"$in": ids} //nolint:goconst
	}
	return paginatedDocuments[VotingProcess](ms.votingProcesses, page, limit, filter, options.Find())
}

// CountVotingProcesses counts the voting processes of an organization, filtered by draft
// state (DraftOnly counts unpublished drafts — used to enforce the MaxDrafts plan limit
// against the new collection). Mirrors CountProcesses but keys on the published flag.
func (ms *MongoStorage) CountVotingProcesses(orgAddress common.Address, draft DraftFilter) (int64, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"orgAddress": orgAddress}
	switch draft {
	case DraftOnly:
		filter["published"] = false
	case PublishedOnly:
		filter["published"] = true
	default:
		// no filter
	}
	return ms.votingProcesses.CountDocuments(ctx, filter)
}

// PublishStaleAfter bounds how long a process may stay in the transient "publishing" state
// before the marker is considered stale — left behind by a crashed or restarted publish
// worker — and becomes reclaimable by a new publish. It is a var so tests can shorten it.
var PublishStaleAfter = 15 * time.Minute

// ClaimVotingProcessForPublish atomically transitions an unpublished process into the
// publishing state (published stays false, but a "publishing" timestamp marker is set) so two
// concurrent publish requests cannot both proceed. It returns true when this call won the
// claim. A marker older than PublishStaleAfter is treated as stale and reclaimable, so a
// crash/restart mid-publish cannot leave a process permanently unclaimable. It is the
// authoritative duplicate-publish guard for voting processes.
func (ms *MongoStorage) ClaimVotingProcessForPublish(id bson.ObjectID) (bool, error) {
	if id == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := time.Now()
	cutoff := now.Add(-PublishStaleAfter)
	filter := bson.M{
		"_id":       id,
		"published": false, //nolint:goconst
		"$or": bson.A{ //nolint:goconst
			bson.M{"publishing": bson.M{"$exists": false}}, //nolint:goconst
			bson.M{"publishing": bson.M{"$lt": cutoff}},    //nolint:goconst
		},
	}
	res, err := ms.votingProcesses.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"publishing": now}}) //nolint:goconst
	if err != nil {
		return false, fmt.Errorf("failed to claim voting process for publish: %w", err)
	}
	// The filter is what decides the claim, so matching it is winning it. ModifiedCount would not
	// be: Mongo stores dates to the millisecond, so reclaiming a stale marker inside the same
	// millisecond writes the value already there and the server reports nothing modified — a won
	// claim reported as lost.
	return res.MatchedCount == 1, nil
}

// StaleVotingProcesses returns the ids of processes whose publishing marker is stale (older
// than PublishStaleAfter): a worker crashed or the service restarted mid-publish. The caller
// clears the marker and resets their questions (see the startup reconciliation).
func (ms *MongoStorage) StaleVotingProcesses() ([]bson.ObjectID, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cutoff := time.Now().Add(-PublishStaleAfter)
	cur, err := ms.votingProcesses.Find(ctx,
		bson.M{"published": false, "publishing": bson.M{"$exists": true, "$lt": cutoff}},
		options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("failed to query stale publishing processes: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()
	var out []bson.ObjectID
	for cur.Next(ctx) {
		var doc struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode stale process id: %w", err)
		}
		out = append(out, doc.ID)
	}
	return out, cur.Err()
}

// ClearVotingProcessPublishing clears the transient publishing marker set by
// ClaimVotingProcessForPublish, so a publish that fails after claiming does not leave the
// process permanently unclaimable. No-op once the process is published.
func (ms *MongoStorage) ClearVotingProcessPublishing(id bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": id, "published": false}
	if _, err := ms.votingProcesses.UpdateOne(ctx, filter, bson.M{"$unset": bson.M{"publishing": ""}}); err != nil { //nolint:goconst
		return fmt.Errorf("failed to clear voting process publishing state: %w", err)
	}
	return nil
}

// SetVotingProcessPublished marks a process as published and clears the publishing marker.
// Called once, atomically, after every question of the process has been confirmed on-chain.
// startDate is the actual start of the on-chain elections (the chain assigns one when the
// process was created without a start date, meaning "start immediately"); a non-zero value
// replaces the stored date so reads expose when the process really started, while a zero
// value leaves the stored date untouched.
func (ms *MongoStorage) SetVotingProcessPublished(id bson.ObjectID, startDate time.Time) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	set := bson.M{"published": true, "updatedAt": time.Now()}
	if !startDate.IsZero() {
		set["startDate"] = startDate
	}
	update := bson.M{"$set": set, "$unset": bson.M{"publishing": ""}} //nolint:goconst
	res, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return fmt.Errorf("failed to mark voting process published: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}
