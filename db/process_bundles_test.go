package db

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// insertLegacyBundle seeds a legacy bundle row directly: the bundle writers are gone, only the
// readers and teardown remain.
func insertLegacyBundle(c *qt.C, b *ProcessesBundle) {
	b.ID = bson.NewObjectID()
	_, err := testDB.processBundles.InsertOne(context.Background(), b)
	c.Assert(err, qt.IsNil)
}

func TestProcessBundleReaders(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	p1, p2, p3 := internal.HexBytesFromString("0x1111"), internal.HexBytesFromString("0x2222"),
		internal.HexBytesFromString("0x3333")
	b1 := &ProcessesBundle{OrgAddress: testOrgAddress, Processes: []internal.HexBytes{p1, p2}}
	b2 := &ProcessesBundle{OrgAddress: testOrgAddress, Processes: []internal.HexBytes{p2, p3}}
	insertLegacyBundle(c, b1)
	insertLegacyBundle(c, b2)

	got, err := testDB.ProcessBundle(b1.ID[:])
	c.Assert(err, qt.IsNil)
	c.Assert(got.Processes, qt.DeepEquals, b1.Processes)

	bundles, err := testDB.ProcessBundlesByProcess(p2)
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 2)
	bundles, err = testDB.ProcessBundlesByProcess(p3)
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 1)
	c.Assert(bundles[0].ID, qt.Equals, b2.ID)
	_, err = testDB.ProcessBundlesByProcess(nil)
	c.Assert(err, qt.Equals, ErrInvalidData)
}
