package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// cspTokenIDIndex is the index migration 40 creates on cspTokens.
const cspTokenIDIndex = "csp_tokens_token_id"

// TestCSPChallengeDocumentsMigration checks that migration 40 turns rows shaped as lts stores them
// (the token as _id, a verified boolean) into challenge documents that resolve, verify and keep
// their verification, that it is idempotent, and that it creates the unique partial token index
// and its down migration drops it.
func TestCSPChallengeDocumentsMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig := migrationByVersion(c, 40)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	now := time.Now().Truncate(time.Millisecond)
	pending := internal.HexBytes(uuid.New().String())
	verified := internal.HexBytes(uuid.New().String())
	authOnly := internal.HexBytes(uuid.New().String())
	_, err := testDB.cspTokens.InsertMany(ctx, []any{
		bson.M{
			"_id": pending, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"secret": "secret", "attempts": 0, "verified": false, "verifiedat": time.Time{}, "resends": 0,
		},
		bson.M{
			"_id": verified, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"attempts": 1, "verified": true, "verifiedat": now, "resends": 0,
		},
		bson.M{
			"_id": authOnly, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"attempts": 0, "verified": true, "verifiedat": now,
		},
	})
	c.Assert(err, qt.IsNil)

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	// re-running it over migrated documents changes nothing
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	for _, tc := range []struct {
		token    internal.HexBytes
		verified bool
		secret   string
	}{
		{pending, false, "secret"},
		{verified, true, ""},
		{authOnly, true, ""},
	} {
		data, err := testDB.CSPAuth(tc.token)
		c.Assert(err, qt.IsNil)
		c.Assert(data.ID, qt.DeepEquals, tc.token)
		c.Assert(data.Tokens, qt.HasLen, 1)
		c.Assert(data.Tokens[0].ID, qt.DeepEquals, tc.token)
		c.Assert(data.Tokens[0].CreatedAt.Equal(now), qt.IsTrue)
		c.Assert(data.Verified, qt.Equals, tc.verified)
		c.Assert(data.Secret, qt.Equals, tc.secret)
		c.Assert(data.Sends, qt.HasLen, 0)
	}
	// the pending code still verifies, through its token
	c.Assert(testDB.VerifyCSPAuth(pending), qt.IsNil)
	data, err := testDB.CSPAuth(pending)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Verified, qt.IsTrue)

	// the token index: unique, and partial so rows without tokens never collide as nulls
	type indexSpec struct {
		Name    string   `bson:"name"`
		Key     bson.D   `bson:"key"`
		Unique  bool     `bson:"unique"`
		Partial bson.Raw `bson:"partialFilterExpression"`
	}
	tokenIndex := func() (indexSpec, bool) {
		cursor, err := testDB.cspTokens.Indexes().List(ctx)
		c.Assert(err, qt.IsNil)
		var indexes []indexSpec
		c.Assert(cursor.All(ctx, &indexes), qt.IsNil)
		for _, idx := range indexes {
			if idx.Name == cspTokenIDIndex {
				return idx, true
			}
		}
		return indexSpec{}, false
	}
	idx, ok := tokenIndex()
	c.Assert(ok, qt.IsTrue)
	c.Assert(idx.Key, qt.DeepEquals, bson.D{{Key: "tokens.id", Value: int32(1)}})
	c.Assert(idx.Unique, qt.IsTrue)
	// compared as raw bson: the driver decodes the nested document into a map, not a bson.D
	wantPartial, err := bson.Marshal(bson.D{{Key: "tokens.id", Value: bson.D{{Key: "$exists", Value: true}}}})
	c.Assert(err, qt.IsNil)
	c.Assert(idx.Partial, qt.DeepEquals, bson.Raw(wantPartial))

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	_, ok = tokenIndex()
	c.Assert(ok, qt.IsFalse)
	// dropping an index already gone must not fail
	c.Assert(mig.Down(ctx, database), qt.IsNil)
}
