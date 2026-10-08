package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MempoolTxTTL is how long the Vochain may keep a submitted tx in its mempool before evicting it:
// transactionBlocksTTL (60 blocks) in vocdoni-node's vochain/app.go, at its ~10s block time. A tx
// that is not mined by then never will be.
const MempoolTxTTL = 60 * 10 * time.Second

// PendingMetadataFinalAfter is how long after its tx was submitted a pending metadata edit is
// final: its tx mined, or it never will. It adds a margin to MempoolTxTTL for slow blocks.
var PendingMetadataFinalAfter = MempoolTxTTL + 5*time.Minute

// MetadataUpdateStaleAfter bounds how long a metadata update claim may stay on a process before it
// is considered left behind by a crashed or restarted backend and becomes reclaimable. The claim is
// held until every pending edit it put on chain is final, so this lies just past
// PendingMetadataFinalAfter: by then whatever the claim covered is final and can be settled.
var MetadataUpdateStaleAfter = PendingMetadataFinalAfter + time.Minute

// ClaimVotingProcessMetadataUpdate atomically marks a published process as having a metadata
// update in flight, so a second update cannot be enqueued until every tx of the first one is final
// and the claim is cleared, which keeps at most one pending version per question. It returns true
// when this call won the claim. A claim older than MetadataUpdateStaleAfter is reclaimable, so a
// crash mid-job cannot block edits forever.
func (ms *MongoStorage) ClaimVotingProcessMetadataUpdate(id bson.ObjectID) (bool, error) {
	if id == bson.NilObjectID {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := time.Now()
	filter := bson.M{
		"_id":       id,
		"published": true,
		"$or": bson.A{
			bson.M{"metadataUpdating": bson.M{"$exists": false}},
			bson.M{"metadataUpdating": bson.M{"$lt": now.Add(-MetadataUpdateStaleAfter)}},
		},
	}
	res, err := ms.votingProcesses.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"metadataUpdating": now}})
	if err != nil {
		return false, fmt.Errorf("failed to claim voting process for metadata update: %w", err)
	}
	// matching the filter is winning the claim (see ClaimVotingProcessForPublish on ModifiedCount)
	return res.MatchedCount == 1, nil
}

// ClearVotingProcessMetadataUpdate releases the claim taken by ClaimVotingProcessMetadataUpdate.
func (ms *MongoStorage) ClearVotingProcessMetadataUpdate(id bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$unset": bson.M{"metadataUpdating": ""}}); err != nil {
		return fmt.Errorf("failed to clear voting process metadata update claim: %w", err)
	}
	return nil
}

// SetVotingProcessText writes the process's own editable text in one targeted update, leaving
// every other field — the publish and metadata claims included — untouched. updatedAt only moves
// forward ($max), keeping the conditional-update token of SetVotingProcessDraft monotonic.
func (ms *MongoStorage) SetVotingProcessText(id bson.ObjectID, text *ProcessText) error {
	if text == nil {
		return ErrInvalidData
	}
	return ms.SetVotingProcessMetadata(id, &ProcessMetadataUpdate{Text: *text})
}

// SetVotingProcessMetadata writes an edit of the process's own text in one targeted update, as
// SetVotingProcessText. An update that repoints the parent election at a new metadata hash also
// stores its URL and hash and drops the parent's pending edit, which that version resolves.
func (ms *MongoStorage) SetVotingProcessMetadata(id bson.ObjectID, u *ProcessMetadataUpdate) error {
	if id == bson.NilObjectID || u == nil {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	set := bson.M{"title": u.Text.Title}
	unset := bson.M{}
	setOrUnset(set, unset, "description", u.Text.Description)
	setOrUnset(set, unset, "header", u.Text.Header)
	setOrUnset(set, unset, "streamUri", u.Text.StreamURI)
	if len(u.MetadataHash) > 0 {
		set["metadataURL"] = u.MetadataURL
		set["metadataHash"] = u.MetadataHash
		unset["pendingMetadata"] = ""
	}
	update := bson.M{"$set": set, "$max": bson.M{"updatedAt": time.Now()}}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	res, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return fmt.Errorf("failed to set voting process text: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetQuestionText writes an edit of one question's display content in one targeted update. It
// only applies while the stored question still has exactly len(ChoiceTitles) choices, since the
// titles are matched by position; ErrNotFound means the question is gone or its choices changed.
// An update that repoints the question at a new metadata hash also drops its pending edit, which
// that version resolves.
func (ms *MongoStorage) SetQuestionText(u *QuestionTextUpdate) error {
	if u == nil || u.ID == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	set := bson.M{"title": u.Title}
	unset := bson.M{}
	setOrUnset(set, unset, "description", u.Description)
	for i, title := range u.ChoiceTitles {
		set[fmt.Sprintf("choices.%d.title", i)] = title
	}
	if u.Metadata != nil {
		set["metadata"] = u.Metadata
	}
	if u.MetadataURL != "" {
		set["metadataURL"] = u.MetadataURL
	}
	if len(u.MetadataHash) > 0 {
		set["metadataHash"] = u.MetadataHash
		unset["pendingMetadata"] = ""
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	filter := bson.M{"_id": u.ID, "choices": bson.M{"$size": len(u.ChoiceTitles)}}
	res, err := ms.processesQuestions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to set question text: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetQuestionPendingMetadata records the edit a question's SET_PROCESS_METADATA tx is about to put
// on chain, replacing any earlier pending edit.
func (ms *MongoStorage) SetQuestionPendingMetadata(id bson.ObjectID, pending *PendingQuestionMetadata) error {
	if id == bson.NilObjectID || pending == nil {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"pendingMetadata": pending}})
	if err != nil {
		return fmt.Errorf("failed to set question pending metadata: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearQuestionPendingMetadata drops a question's pending edit, once its tx is known not to land.
func (ms *MongoStorage) ClearQuestionPendingMetadata(id bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$unset": bson.M{"pendingMetadata": ""}}); err != nil {
		return fmt.Errorf("failed to clear question pending metadata: %w", err)
	}
	return nil
}

// SetVotingProcessPendingMetadata records the edit the parent election's SET_PROCESS_METADATA tx is
// about to put on chain, replacing any earlier pending edit.
func (ms *MongoStorage) SetVotingProcessPendingMetadata(id bson.ObjectID, pending *PendingProcessMetadata) error {
	if id == bson.NilObjectID || pending == nil {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"pendingMetadata": pending}})
	if err != nil {
		return fmt.Errorf("failed to set voting process pending metadata: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearVotingProcessPendingMetadata drops the parent election's pending edit, once its tx is known
// not to land.
func (ms *MongoStorage) ClearVotingProcessPendingMetadata(id bson.ObjectID) error {
	if id == bson.NilObjectID {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.votingProcesses.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$unset": bson.M{"pendingMetadata": ""}}); err != nil {
		return fmt.Errorf("failed to clear voting process pending metadata: %w", err)
	}
	return nil
}

// setOrUnset adds key to set with value, or to unset when value is an empty string or text,
// matching the omitempty encoding a whole-document write would have produced.
func setOrUnset(set, unset bson.M, key string, value any) {
	switch v := value.(type) {
	case string:
		if v == "" {
			unset[key] = ""
			return
		}
	case MultiLangString:
		if len(v) == 0 {
			unset[key] = ""
			return
		}
	default:
		// any other value is always set
	}
	set[key] = value
}
