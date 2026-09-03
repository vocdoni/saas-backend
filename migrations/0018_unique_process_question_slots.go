package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(18, "unique_process_question_slots", upUniqueProcessQuestionSlots, downUniqueProcessQuestionSlots)
}

// upUniqueProcessQuestionSlots makes (processId, order) a storage-level key: it repairs the
// processes that a pre-fix concurrent draft update duplicated (issue #614), then enforces
// uniqueness so no future writer — including a direct database write — can reintroduce that state.
//
// The dedupe has to run first, because creating a unique index over existing duplicates fails.
// That same pass is the bulk repair of already-corrupted drafts, so the two halves are one unit.
//
// The plain processId index from migration 0017 is dropped: the compound index below serves the
// same prefix queries.
func upUniqueProcessQuestionSlots(ctx context.Context, database *mongo.Database) error {
	return replaceIndexWithUpdateFunc(
		ctx, database.Collection("processesQuestions"),
		[]string{"processId_1"},
		[]mongo.IndexModel{{
			Keys:    bson.D{{Key: "processId", Value: 1}, {Key: "order", Value: 1}}, //nolint:goconst
			Options: options.Index().SetUnique(true),
		}},
		func() error { return dedupeQuestionSlots(ctx, database) },
	)
}

func downUniqueProcessQuestionSlots(ctx context.Context, database *mongo.Database) error {
	// the repaired rows are not restored: the duplicates were the bug. Only the index reverts.
	return replaceIndex(
		ctx, database.Collection("processesQuestions"),
		[]string{"processId_1_order_1"},
		[]mongo.IndexModel{{Keys: bson.D{{Key: "processId", Value: 1}}}},
	)
}

// questionSlotKey identifies one question slot of a process.
type questionSlotKey struct {
	ProcessID bson.ObjectID `bson:"processId"`
	Order     int           `bson:"order"`
}

// questionSlotRow is one row found in a slot, with what decides whether it may be deleted.
type questionSlotRow struct {
	ID         bson.ObjectID `bson:"id"`
	UpstreamID []byte        `bson:"upstreamId"`
}

// questionSlotGroup is one (processId, order) slot holding more than one row.
type questionSlotGroup struct {
	Key  questionSlotKey   `bson:"_id"`
	Rows []questionSlotRow `bson:"rows"`
}

// dedupeQuestionSlots leaves exactly one row per (processId, order).
//
// A row that carries an upstreamId is a published question: a real on-chain election exists for it
// and deleting the row would hide that election from the API forever. Those are never deleted —
// the extras are moved to free slots at the tail of the process instead, which loses the intended
// ordering of an already-broken process but no data. Only unpublished rows are deleted, and the
// keeper is the one the parent process names in questionIds (the set every reader agrees on),
// falling back to the oldest row.
func dedupeQuestionSlots(ctx context.Context, database *mongo.Database) error {
	questions := database.Collection("processesQuestions")
	cursor, err := questions.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
		{{Key: "$group", Value: bson.M{
			"_id":  bson.M{"processId": "$processId", "order": "$order"},
			"rows": bson.M{"$push": bson.M{"id": "$_id", "upstreamId": "$upstreamId"}},
		}}},
		{{Key: "$match", Value: bson.M{"rows.1": bson.M{"$exists": true}}}},
	})
	if err != nil {
		return fmt.Errorf("failed to scan for duplicated question slots: %w", err)
	}
	var groups []questionSlotGroup
	if err := cursor.All(ctx, &groups); err != nil {
		return fmt.Errorf("failed to decode duplicated question slots: %w", err)
	}
	if len(groups) == 0 {
		return nil
	}
	// next free slot per process, so a published extra can be moved rather than dropped
	tail := map[bson.ObjectID]int{}
	for _, g := range groups {
		pid := g.Key.ProcessID
		listed, err := processQuestionIDs(ctx, database, pid)
		if err != nil {
			return err
		}
		if _, ok := tail[pid]; !ok {
			next, err := nextFreeOrder(ctx, questions, pid)
			if err != nil {
				return err
			}
			tail[pid] = next
		}
		keep := pickKeeper(g, listed)
		for _, row := range g.Rows {
			if row.ID == keep {
				continue
			}
			if len(row.UpstreamID) > 0 {
				// published: relocate, never delete
				if _, err := questions.UpdateByID(ctx, row.ID,
					bson.M{"$set": bson.M{"order": tail[pid]}}); err != nil {
					return fmt.Errorf("failed to relocate published question %s: %w", row.ID.Hex(), err)
				}
				log.Warnw("relocated a duplicated published question",
					"processId", pid.Hex(), "questionId", row.ID.Hex(),
					"fromOrder", g.Key.Order, "toOrder", tail[pid])
				tail[pid]++
				continue
			}
			if _, err := questions.DeleteOne(ctx, bson.M{"_id": row.ID}); err != nil {
				return fmt.Errorf("failed to remove duplicated question %s: %w", row.ID.Hex(), err)
			}
			log.Infow("removed a duplicated draft question",
				"processId", pid.Hex(), "questionId", row.ID.Hex(), "order", g.Key.Order)
		}
	}
	return nil
}

// pickKeeper chooses the row that keeps the slot: a published one first (it has an on-chain
// election), then the one the process itself lists, then the oldest.
func pickKeeper(g questionSlotGroup, listed map[bson.ObjectID]bool) bson.ObjectID {
	for _, row := range g.Rows {
		if len(row.UpstreamID) > 0 {
			return row.ID
		}
	}
	for _, row := range g.Rows {
		if listed[row.ID] {
			return row.ID
		}
	}
	return g.Rows[0].ID // sorted by _id, so the oldest
}

// processQuestionIDs is the set of question ids the process itself records, empty for a process
// predating the field.
func processQuestionIDs(
	ctx context.Context, database *mongo.Database, processID bson.ObjectID,
) (map[bson.ObjectID]bool, error) {
	var vp struct {
		QuestionIDs []bson.ObjectID `bson:"questionIds"`
	}
	err := database.Collection("votingProcesses").FindOne(ctx, bson.M{"_id": processID}).Decode(&vp)
	if err != nil && err != mongo.ErrNoDocuments {
		return nil, fmt.Errorf("failed to read process %s: %w", processID.Hex(), err)
	}
	listed := make(map[bson.ObjectID]bool, len(vp.QuestionIDs))
	for _, id := range vp.QuestionIDs {
		listed[id] = true
	}
	return listed, nil
}

// nextFreeOrder is one past the highest slot currently used by the process.
func nextFreeOrder(ctx context.Context, questions *mongo.Collection, processID bson.ObjectID) (int, error) {
	var highest struct {
		Order int `bson:"order"`
	}
	err := questions.FindOne(ctx, bson.M{"processId": processID},
		options.FindOne().SetSort(bson.D{{Key: "order", Value: -1}})).Decode(&highest)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read the highest question order of %s: %w", processID.Hex(), err)
	}
	return highest.Order + 1, nil
}
