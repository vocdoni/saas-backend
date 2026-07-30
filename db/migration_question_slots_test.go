package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// questionSlotsMigration returns migration 0018.
func questionSlotsMigration(c *qt.C) migrations.Migration {
	mig, ok := migrations.AsMap()[18]
	c.Assert(ok, qt.IsTrue)
	return mig
}

// TestUniqueProcessQuestionSlotsMigration seeds the duplicate slots a pre-fix concurrent draft
// update stranded (issue #614) and asserts migration 0018 repairs them under its stated policy:
// published rows are never deleted (the keeper wins the slot, extras relocate to the tail), the
// keeper of an unpublished slot is the row the process lists in questionIds, everything else is
// deleted — and the unique (processId, order) index then makes the state unrepresentable.
func TestUniqueProcessQuestionSlotsMigration(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx := context.Background()
	mig := questionSlotsMigration(c)
	database := testDB.DBClient.Database(testDB.database)

	// the test database is migrated on init, so the unique index already exists; roll it back to
	// the pre-migration state (plain processId index) so duplicates can be seeded at all
	c.Assert(mig.Down(ctx, database), qt.IsNil)

	org := common.Address{0x61, 0x18}
	setupVotingProcessOrg(c, org)
	pid, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Title: MultiLangString{"default": "P"}})
	c.Assert(err, qt.IsNil)
	ids, err := testDB.SetProcessQuestions(pid, questionSet(org, 2, "listed"))
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.SetVotingProcessQuestionIDs(pid, ids), qt.IsNil)

	// seed the duplicates directly, bypassing the write chokepoint that now prevents them.
	// slot 0 gains two published extras: the older one (pubOld) must take the slot, the newer one
	// (pubNew) must be relocated to the tail, and the listed-but-unpublished original is deleted.
	pubOld := seedQuestionRow(c, pid, org, 0, []byte{0xaa, 0x01}, time.Now().Add(-2*time.Hour))
	pubNew := seedQuestionRow(c, pid, org, 0, []byte{0xaa, 0x02}, time.Now().Add(-time.Hour))
	// slot 1 gains an unpublished extra that is OLDER than the listed row: the listed row must
	// still win the slot (listed beats oldest), and the extra is deleted
	unlisted := seedQuestionRow(c, pid, org, 1, nil, time.Now().Add(-time.Hour))

	c.Assert(mig.Up(ctx, database), qt.IsNil)

	stored, err := testDB.QuestionsByProcess(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.HasLen, 3)
	c.Assert(stored[0].Order, qt.Equals, 0)
	c.Assert(stored[0].ID, qt.Equals, pubOld, qt.Commentf("the oldest published row must keep the slot"))
	c.Assert(stored[1].Order, qt.Equals, 1)
	c.Assert(stored[1].ID, qt.Equals, ids[1], qt.Commentf("the row listed in questionIds must beat an older unlisted one"))
	c.Assert(stored[2].Order, qt.Equals, 2)
	c.Assert(stored[2].ID, qt.Equals, pubNew, qt.Commentf("a published extra must be relocated to the tail, not deleted"))
	for i := range stored {
		c.Assert(stored[i].ID, qt.Not(qt.Equals), unlisted)
		c.Assert(stored[i].ID, qt.Not(qt.Equals), ids[0], qt.Commentf("the unpublished original loses slot 0 to a published row"))
	}

	// running the migration again over the repaired state is a no-op
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	again, err := testDB.QuestionsByProcess(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(again, qt.DeepEquals, stored)

	// the state is now unrepresentable: a direct duplicate write fails on the unique index
	_, err = testDB.processesQuestions.InsertOne(ctx, &VotingProcessQuestion{
		ID: primitive.NewObjectID(), ProcessID: pid, OrgAddress: org, Order: 0,
		Type: VotingTypeSingleChoice, Title: MultiLangString{"default": "dup"},
	})
	c.Assert(mongo.IsDuplicateKeyError(err), qt.IsTrue,
		qt.Commentf("expected a duplicate-key error on (processId, order), got %v", err))
}

// seedQuestionRow inserts a question row directly with a chosen creation time (the _id timestamp
// decides which duplicate is "oldest") and, when upstreamID is set, as published.
func seedQuestionRow(
	c *qt.C, pid primitive.ObjectID, org common.Address, order int, upstreamID []byte, createdAt time.Time,
) primitive.ObjectID {
	c.Helper()
	q := &VotingProcessQuestion{
		ID:         primitive.NewObjectIDFromTimestamp(createdAt),
		ProcessID:  pid,
		OrgAddress: org,
		Order:      order,
		Type:       VotingTypeSingleChoice,
		Title:      MultiLangString{"default": "stray"},
		Choices:    []Choice{{Title: MultiLangString{"default": "Yes"}, Value: 0}},
	}
	if len(upstreamID) > 0 {
		q.UpstreamID = upstreamID
		q.Status = QuestionStatusReady
	}
	_, err := testDB.processesQuestions.InsertOne(context.Background(), q)
	c.Assert(err, qt.IsNil)
	return q.ID
}

// TestUniqueProcessQuestionSlotsMigrationDown asserts the down migration restores the plain
// processId index and drops the unique one, so duplicates become representable again.
func TestUniqueProcessQuestionSlotsMigrationDown(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(questionSlotsMigration(c).Up(context.Background(), testDB.DBClient.Database(testDB.database)), qt.IsNil)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})
	ctx := context.Background()
	mig := questionSlotsMigration(c)
	database := testDB.DBClient.Database(testDB.database)

	c.Assert(mig.Down(ctx, database), qt.IsNil)

	cursor, err := testDB.processesQuestions.Indexes().List(ctx)
	c.Assert(err, qt.IsNil)
	var indexes []bson.M
	c.Assert(cursor.All(ctx, &indexes), qt.IsNil)
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, idx["name"].(string))
	}
	c.Assert(names, qt.Contains, "processId_1")
	c.Assert(names, qt.Not(qt.Contains), "processId_1_order_1")
}
