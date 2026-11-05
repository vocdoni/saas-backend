package db

import (
	"context"
	"fmt"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// SetVotingProcessPublishSubmitted sets VotingProcess.PublishSubmitted, only while owner still
// holds the publish claim; ErrConflict means the claim is lost. A publish worker calls it before it
// records and submits its txs.
func (ms *MongoStorage) SetVotingProcessPublishSubmitted(id bson.ObjectID, owner string) error {
	if id == bson.NilObjectID || owner == "" {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.votingProcesses.UpdateOne(ctx,
		bson.M{"_id": id, "publishingOwner": owner},
		bson.M{"$set": bson.M{"publishSubmitted": true}})
	if err != nil {
		return fmt.Errorf("failed to flag voting process publish submitted: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrConflict
	}
	return nil
}

// ClearVotingProcessPublishSubmitted unsets VotingProcess.PublishSubmitted once no question of
// the process without an election holds a PublishTx. It reports false, leaving the flag set,
// while one does. Count then update: the caller holds the publish claim, which keeps any publish
// from appending a record in between.
func (ms *MongoStorage) ClearVotingProcessPublishSubmitted(id bson.ObjectID) (bool, error) {
	if id == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	held, err := ms.processesQuestions.CountDocuments(ctx, bson.M{
		"processId":    id,
		"upstreamId":   bson.M{"$exists": false},
		"publishTxs.0": bson.M{"$exists": true},
	})
	if err != nil {
		return false, fmt.Errorf("failed to count questions holding publish txs: %w", err)
	}
	if held > 0 {
		return false, nil
	}
	if _, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$unset": bson.M{"publishSubmitted": ""}}); err != nil {
		return false, fmt.Errorf("failed to clear voting process publish submitted: %w", err)
	}
	return true, nil
}

// AppendQuestionPublishTx records a tx about to be submitted for a question of processID. It
// matches only while the question holds no record or holds records at the same nonce (the
// same-nonce rule), so a stray write from a worker that lost its claim is a no-op; ErrConflict
// reports that nothing matched.
func (ms *MongoStorage) AppendQuestionPublishTx(processID, questionID bson.ObjectID, tx PublishTx) error {
	if processID == bson.NilObjectID || questionID == bson.NilObjectID || len(tx.Hash) == 0 {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":       questionID,
		"processId": processID,
		"$or": bson.A{
			bson.M{"publishTxs.0": bson.M{"$exists": false}},
			bson.M{"publishTxs.nonce": tx.Nonce},
		},
	}
	res, err := ms.processesQuestions.UpdateOne(ctx, filter, bson.M{"$push": bson.M{"publishTxs": tx}})
	if err != nil {
		return fmt.Errorf("failed to record question publish tx: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrConflict
	}
	return nil
}

// SetQuestionPublishTxUpstreamID stores on the record with the given hash the election id the
// node predicted for it. A record already gone is not an error.
func (ms *MongoStorage) SetQuestionPublishTxUpstreamID(
	questionID bson.ObjectID, hash, upstreamID internal.HexBytes,
) error {
	if questionID == bson.NilObjectID || len(hash) == 0 || len(upstreamID) == 0 {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.UpdateOne(ctx,
		bson.M{"_id": questionID, "publishTxs.hash": hash},
		bson.M{"$set": bson.M{"publishTxs.$.upstreamId": upstreamID}}); err != nil {
		return fmt.Errorf("failed to set publish tx upstream id: %w", err)
	}
	return nil
}

// RemoveQuestionPublishTx drops the record with the given hash: the node reported that tx never
// entered its mempool. Removing the last record leaves an empty array, which counts as holding none.
func (ms *MongoStorage) RemoveQuestionPublishTx(questionID bson.ObjectID, hash internal.HexBytes) error {
	if questionID == bson.NilObjectID || len(hash) == 0 {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": questionID},
		bson.M{"$pull": bson.M{"publishTxs": bson.M{"hash": hash}}}); err != nil {
		return fmt.Errorf("failed to remove question publish tx: %w", err)
	}
	return nil
}

// ClearQuestionPublishTxs unsets a question's records once the chain proved none of them can mine,
// only while they are still the ones at nonce: a record at another nonce was written after the
// caller's reads and is left alone.
func (ms *MongoStorage) ClearQuestionPublishTxs(questionID bson.ObjectID, nonce uint32) error {
	if questionID == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.UpdateOne(ctx,
		bson.M{"_id": questionID, "publishTxs.nonce": nonce},
		bson.M{"$unset": bson.M{"publishTxs": ""}}); err != nil {
		return fmt.Errorf("failed to clear question publish txs: %w", err)
	}
	return nil
}
