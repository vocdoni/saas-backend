package db

import (
	"context"
	"fmt"

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
