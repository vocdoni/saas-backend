package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestUnlinkDeletedGroupCensusesMigration asserts migration 0036 drops a deleted census from the
// groups that still link it and keeps the existing ones.
func TestUnlinkDeletedGroupCensusesMigration(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	mig, ok := migrations.AsMap()[36]
	c.Assert(ok, qt.IsTrue)

	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)
	live, err := testDB.SetCensus(&Census{OrgAddress: testOrgAddress})
	c.Assert(err, qt.IsNil)
	deleted := bson.NewObjectID().Hex()
	groupID := bson.NewObjectID()
	_, err = testDB.orgMemberGroups.InsertOne(t.Context(), &OrganizationMemberGroup{
		ID: groupID, OrgAddress: testOrgAddress, CensusIDs: []string{live, deleted},
	})
	c.Assert(err, qt.IsNil)

	database := testDB.DBClient.Database(testDB.database)
	c.Assert(mig.Up(t.Context(), database), qt.IsNil)
	group, err := testDB.OrganizationMemberGroup(groupID.Hex(), testOrgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(group.CensusIDs, qt.DeepEquals, []string{live})

	// re-running it is a no-op
	c.Assert(mig.Up(t.Context(), database), qt.IsNil)
}
