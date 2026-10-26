package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestClearLegacyMemberPasswordsMigration asserts migration 0031 unsets every member's stored
// password, which predates the client hash and can never match, leaves the rest of the member
// alone, and that its down is a no-op.
func TestClearLegacyMemberPasswordsMigration(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	// seeded raw, the way a release before the client hash stored it: argon2 of the plaintext
	legacy, without := bson.NewObjectID(), bson.NewObjectID()
	_, err := testDB.orgMembers.InsertMany(ctx, []any{
		bson.M{
			"_id": legacy, "orgAddress": testOrgAddress, "memberNumber": "L-1",
			"pass": internal.HashPassword(testSalt, "plaintext"),
		},
		bson.M{"_id": without, "orgAddress": testOrgAddress, "memberNumber": "L-2"},
	})
	c.Assert(err, qt.IsNil)

	raw := func(id bson.ObjectID) bson.M {
		m := bson.M{}
		c.Assert(testDB.orgMembers.FindOne(ctx, bson.M{"_id": id}).Decode(&m), qt.IsNil)
		return m
	}

	mig := migrationByVersion(c, 31)
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	for _, id := range []bson.ObjectID{legacy, without} {
		m := raw(id)
		_, hasPass := m["pass"]
		c.Assert(hasPass, qt.IsFalse, qt.Commentf("member %s", id.Hex()))
	}
	c.Assert(raw(legacy)["memberNumber"], qt.Equals, "L-1")
	member, err := testDB.OrgMember(testOrgAddress, legacy.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(member.HashedPass, qt.HasLen, 0)

	// re-running finds nothing to clear, and the down leaves the members as they are
	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(mig.Down(ctx, database), qt.IsNil)
	_, hasPass := raw(legacy)["pass"]
	c.Assert(hasPass, qt.IsFalse)
}
