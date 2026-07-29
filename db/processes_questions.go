package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SetQuestion creates or replaces a voting-process question document. It assigns an ID on
// first insert. Questions are referenced (ordered) from their parent VotingProcess.
func (ms *MongoStorage) SetQuestion(q *VotingProcessQuestion) (primitive.ObjectID, error) {
	if q.ProcessID == primitive.NilObjectID {
		return primitive.NilObjectID, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if q.ID.IsZero() {
		q.ID = primitive.NewObjectID()
	}
	filter := bson.M{"_id": q.ID} //nolint:goconst
	opts := options.Replace().SetUpsert(true)
	if _, err := ms.processesQuestions.ReplaceOne(ctx, filter, q, opts); err != nil {
		return primitive.NilObjectID, fmt.Errorf("failed to create or update question: %w", err)
	}
	return q.ID, nil
}

// SetProcessQuestions replaces a process's questions, addressing each by the slot it occupies:
// question i is upserted on (processId, order=i), and anything beyond the new length is deleted.
// It returns the resulting ids in order, for the process's own QuestionIDs.
//
// This exists instead of delete-everything-then-insert-everything because that sequence is not safe
// against a second writer. Two overlapping draft updates interleave so that one deletes rows the
// other has not inserted yet, and the stragglers survive as duplicates carrying the same processId —
// which publish then turns into real elections (issue #614). Addressing rows by slot means neither
// writer deletes what the other is inserting.
//
// It also keeps a question's _id stable across draft edits, since an existing row in a slot is
// replaced rather than dropped and re-created. Publish, the status syncer and the eligibility lists
// all key off those ids.
//
// The final sweep deletes every other row of the process, not just the tail past the new length, so
// re-saving a draft repairs a database that a pre-fix update had already duplicated.
//
// What concurrent writers can and cannot do to a slot:
//
//   - A slot that already holds a row is replaced atomically. The worst two writers do to it is
//     leave it belonging to whichever wrote last, keeping the _id it already had — a state some
//     client actually asked for.
//   - A slot that does not exist yet is not protected. FindOneAndReplace is atomic only for a
//     matched document, and there is no unique index on (processId, order), so two callers racing on
//     an empty slot can both insert. Each one's sweep then deletes the other's row, leaving the slot
//     empty and the process's QuestionIDs naming an id that no longer exists.
//   - Either way at most one row per slot survives, so the residual failure loses a row rather than
//     duplicating one. A row survives only if every other writer's sweep ran before that row was
//     inserted, while each writer's own sweep always runs after its own insert; two survivors in one
//     slot would need each writer's sweep to precede the other's, which cannot both hold.
//     Duplication is the failure mode of issue #614, and that one is closed.
//
// Losing a row is fail-safe: the stored set no longer matches the process's QuestionIDs, so
// questionSetProblem refuses the publish and saving the draft again repairs it. Closing the hole for
// real needs a unique index on (processId, order), which is what makes each slot upsert genuinely
// atomic. A transaction is not an option here — the repo opens no session anywhere, and the tests run
// a standalone mongo:7.
//
// Note this is not atomic across slots either: a concurrent reader can still observe a half-applied
// update.
func (ms *MongoStorage) SetProcessQuestions(
	processID primitive.ObjectID, questions []*VotingProcessQuestion,
) ([]primitive.ObjectID, error) {
	if processID == primitive.NilObjectID {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	ids := make([]primitive.ObjectID, 0, len(questions))
	for i, q := range questions {
		q.ProcessID = processID
		q.Order = i
		// the replacement carries no _id, so an existing row in this slot keeps the one it has and a
		// brand new slot gets a server-generated one. Doing it in a single atomic find-and-replace
		// (rather than reading the slot first) leaves no window for a second writer to insert
		// between the read and the write, which would make the replacement fail on the immutable _id.
		doc, err := questionDocWithoutID(q)
		if err != nil {
			return nil, fmt.Errorf("failed to encode question %d: %w", i, err)
		}
		filter := bson.M{"processId": processID, "order": i} //nolint:goconst
		stored := struct {
			ID primitive.ObjectID `bson:"_id"`
		}{}
		err = ms.processesQuestions.FindOneAndReplace(ctx, filter, doc,
			options.FindOneAndReplace().
				SetUpsert(true).
				SetReturnDocument(options.After).
				SetProjection(bson.M{"_id": 1}),
		).Decode(&stored)
		if err != nil {
			return nil, fmt.Errorf("failed to store question %d: %w", i, err)
		}
		q.ID = stored.ID
		ids = append(ids, q.ID)
	}
	// drop anything else still carrying this processId: the tail of a longer previous version, and
	// any duplicate a pre-fix concurrent update had stranded in a slot we just rewrote
	if _, err := ms.processesQuestions.DeleteMany(ctx, bson.M{
		"processId": processID,
		"_id":       bson.M{"$nin": ids},
	}); err != nil {
		return nil, fmt.Errorf("failed to remove surplus questions: %w", err)
	}
	return ids, nil
}

// questionDocWithoutID encodes a question for storage with its _id stripped, so a replacement
// neither overwrites the id of the row it lands on nor fails on the immutable field.
func questionDocWithoutID(q *VotingProcessQuestion) (bson.M, error) {
	raw, err := bson.Marshal(q)
	if err != nil {
		return nil, err
	}
	doc := bson.M{}
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	delete(doc, "_id")
	return doc, nil
}

// Question returns a single question by its hex ObjectID.
func (ms *MongoStorage) Question(id primitive.ObjectID) (*VotingProcessQuestion, error) {
	if id == primitive.NilObjectID {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	q := &VotingProcessQuestion{}
	if err := ms.processesQuestions.FindOne(ctx, bson.M{"_id": id}).Decode(q); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get question: %w", err)
	}
	return q, nil
}

// QuestionsByProcess returns every question of a process, ordered by the Order field.
func (ms *MongoStorage) QuestionsByProcess(processID primitive.ObjectID) ([]VotingProcessQuestion, error) {
	if processID == primitive.NilObjectID {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.processesQuestions.Find(ctx, bson.M{"processId": processID}, //nolint:goconst
		options.Find().SetSort(bson.D{{Key: "order", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch questions by process: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()
	var out []VotingProcessQuestion
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("failed to decode questions: %w", err)
	}
	return out, nil
}

// QuestionByUpstreamID resolves a question from its on-chain election id. Used by the vote
// relay and the status syncer to map an election back to its process/organization.
func (ms *MongoStorage) QuestionByUpstreamID(upstreamID internal.HexBytes) (*VotingProcessQuestion, error) {
	if len(upstreamID) == 0 {
		return nil, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	q := &VotingProcessQuestion{}
	if err := ms.processesQuestions.FindOne(ctx, bson.M{"upstreamId": upstreamID}).Decode(q); err != nil { //nolint:goconst
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get question by upstream id: %w", err)
	}
	return q, nil
}

// SetQuestionPublished records the on-chain outcome of a single question in one targeted
// update, leaving sibling questions untouched.
func (ms *MongoStorage) SetQuestionPublished(
	id primitive.ObjectID, upstreamID internal.HexBytes, metadataURL, status string,
) error {
	if id == primitive.NilObjectID || len(upstreamID) == 0 {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	update := bson.M{"$set": bson.M{"upstreamId": upstreamID, "metadataURL": metadataURL, "status": status}} //nolint:goconst
	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return fmt.Errorf("failed to set question published: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetQuestionStatus sets only the status field of a question (targeted update). Used by the
// status-change handlers (optimistic write) and, later, the status syncer.
func (ms *MongoStorage) SetQuestionStatus(id primitive.ObjectID, status string) error {
	if id == primitive.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": status}})
	if err != nil {
		return fmt.Errorf("failed to set question status: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetQuestionEncryptionKeys sets only the encryptionKeys field of a question (targeted update),
// leaving every other field untouched. The vote-encryption public keys are immutable once published
// by the keykeepers, so this is written once and only with a non-empty set (mirrors
// SetProcessEncryptionKeys for the legacy single-election model).
func (ms *MongoStorage) SetQuestionEncryptionKeys(id primitive.ObjectID, keys []EncryptionKey) error {
	// keys are immutable and written once; reject an empty set so a stray call can never clobber
	// already-cached keys with an empty array (matches the "non-empty" contract above).
	if id == primitive.NilObjectID || len(keys) == 0 {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"encryptionKeys": keys}})
	if err != nil {
		return fmt.Errorf("failed to set question encryption keys: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteQuestion removes a single question document (used when replacing a draft's
// questions on update).
func (ms *MongoStorage) DeleteQuestion(id primitive.ObjectID) error {
	if id == primitive.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		return fmt.Errorf("failed to delete question: %w", err)
	}
	return nil
}

// ResetQuestionsPublish clears the publish state (status, metadataURL) of the not-yet-mined
// questions of a process (those without an upstreamId). Used to abandon a failed publish while
// keeping any elections already on-chain, so a subsequent publish resumes the remaining ones.
func (ms *MongoStorage) ResetQuestionsPublish(processID primitive.ObjectID) error {
	if processID == primitive.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// only reset questions that were NOT mined (no upstreamId): a failed publish must keep the
	// elections already on-chain so a re-publish resumes the remaining ones instead of
	// regenerating (and orphaning) the mined ones.
	filter := bson.M{"processId": processID, "upstreamId": bson.M{"$exists": false}} //nolint:goconst
	update := bson.M{"$unset": bson.M{"status": "", "metadataURL": ""}}              //nolint:goconst
	if _, err := ms.processesQuestions.UpdateMany(ctx, filter, update); err != nil {
		return fmt.Errorf("failed to reset questions publish state: %w", err)
	}
	return nil
}

// SyncableQuestionsByOrg returns the minimal refs of an organization's published questions whose
// stored status can still change on-chain (READY|PAUSED|ENDED). Terminal statuses (CANCELED|RESULTS)
// are final and excluded. It backs the managed-org delete guard, which reads each ref's live chain
// status synchronously at delete time (projected to upstreamId, orgAddress, status).
func (ms *MongoStorage) SyncableQuestionsByOrg(orgAddress common.Address) ([]QuestionStatusRef, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"orgAddress": orgAddress,                                                                              //nolint:goconst
		"upstreamId": bson.M{"$exists": true},                                                                 //nolint:goconst
		"status":     bson.M{"$in": []string{QuestionStatusReady, QuestionStatusPaused, QuestionStatusEnded}}, //nolint:goconst
	}
	proj := options.Find().SetProjection(bson.M{"upstreamId": 1, "orgAddress": 1, "status": 1}) //nolint:goconst
	cur, err := ms.processesQuestions.Find(ctx, filter, proj)
	if err != nil {
		return nil, fmt.Errorf("failed to list syncable questions: %w", err)
	}
	var refs []QuestionStatusRef
	if err := cur.All(ctx, &refs); err != nil {
		return nil, fmt.Errorf("failed to decode syncable questions: %w", err)
	}
	return refs, nil
}

// SetQuestionStatusSynced conditionally reconciles a question (by on-chain id) to next, stamping
// syncedAt. The update applies only while the stored status still equals prev, so a concurrent
// direct write (publish/status-change) is never clobbered by a stale syncer value — a mismatch
// yields matched=false and the caller skips. Passing prev == next just refreshes syncedAt. Used by
// the status syncer.
func (ms *MongoStorage) SetQuestionStatusSynced(upstreamID internal.HexBytes, prev, next string) (bool, error) {
	if len(upstreamID) == 0 {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"upstreamId": upstreamID, "status": prev}               //nolint:goconst
	update := bson.M{"$set": bson.M{"status": next, "syncedAt": time.Now()}} //nolint:goconst
	res, err := ms.processesQuestions.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, fmt.Errorf("failed to set synced question status: %w", err)
	}
	return res.MatchedCount > 0, nil
}
