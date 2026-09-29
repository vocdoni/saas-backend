package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// insertLegacyProcess seeds a legacy process row directly: the /process writers are gone, only
// the readers and teardown remain.
func insertLegacyProcess(c *qt.C, p *Process) {
	p.ID = bson.NewObjectID()
	_, err := testDB.processes.InsertOne(context.Background(), p)
	c.Assert(err, qt.IsNil)
}

func TestProcessReaders(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	published := &Process{
		Address:    internal.HexBytesFromString("0x1111"),
		OrgAddress: testOrgAddress,
		Metadata:   map[string]any{"key1": "value1"},
	}
	insertLegacyProcess(c, published)
	// the legacy writer omitted a draft's address field entirely, which is what DraftOnly matches
	_, err := testDB.processes.InsertOne(context.Background(), bson.M{"_id": bson.NewObjectID(), "orgAddress": testOrgAddress})
	c.Assert(err, qt.IsNil)

	got, err := testDB.Process(published.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Metadata, qt.DeepEquals, published.Metadata)
	_, err = testDB.Process(bson.NewObjectID())
	c.Assert(err, qt.Equals, ErrNotFound)
	_, err = testDB.Process(bson.NilObjectID)
	c.Assert(err, qt.Equals, ErrInvalidData)

	got, err = testDB.ProcessByAddress(published.Address)
	c.Assert(err, qt.IsNil)
	c.Assert(got.ID, qt.Equals, published.ID)
	_, err = testDB.ProcessByAddress(internal.HexBytesFromString("0x2222"))
	c.Assert(err, qt.Equals, ErrNotFound)

	n, err := testDB.CountProcesses(testOrgAddress, DraftOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))
	all, err := testDB.AllProcessesByOrg(testOrgAddress, PublishedOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(all, qt.HasLen, 1)
	c.Assert(all[0].ID, qt.Equals, published.ID)
}
