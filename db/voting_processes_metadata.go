package db

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MetadataUpdateStaleAfter bounds how long a metadata update claim may stay on a process before it
// is considered left behind by a crashed or restarted worker and becomes reclaimable. A job submits
// its txs in one batch and waits at most a few blocks per question, far below this.
var MetadataUpdateStaleAfter = 15 * time.Minute

// ClaimVotingProcessMetadataUpdate atomically marks a published process as having a metadata
// update in flight, so a second update cannot be enqueued until the first one finishes and clears
// it. It returns true when this call won the claim. A claim older than MetadataUpdateStaleAfter is
// reclaimable, so a crash mid-job cannot block edits forever.
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
	if id == bson.NilObjectID || text == nil {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	set := bson.M{"title": text.Title}
	unset := bson.M{}
	setOrUnset(set, unset, "description", text.Description)
	setOrUnset(set, unset, "header", text.Header)
	setOrUnset(set, unset, "streamUri", text.StreamURI)
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

// SetQuestionText writes a text-only edit of one question in one targeted update. It only applies
// while the stored question still has exactly len(ChoiceTitles) choices, since the titles are
// matched by position; ErrNotFound means the question is gone or its choices changed.
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
	if u.MetadataURL != "" {
		set["metadataURL"] = u.MetadataURL
	}
	if len(u.MetadataHash) > 0 {
		set["metadataHash"] = u.MetadataHash
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
