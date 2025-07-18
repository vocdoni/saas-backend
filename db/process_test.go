package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	testProcessID       = internal.HexBytes("test_process_id")
	testProcessRoot     = "0xabcde"
	testProcessURI      = "test_process_uri"
	testProcessMetadata = []byte("test_metadata")
)

func setupTestPrerequisites1(c *qt.C, db *MongoStorage) *Census {
	// Create test organization
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	var rootHex internal.HexBytes
	if err := rootHex.ParseString(testProcessRoot); err != nil {
		c.Assert(err, qt.Not(qt.IsNil))
	}

	// Create test census
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
		Published: PublishedCensus{
			Root: rootHex,
			URI:  testProcessURI,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)
	census.ID, err = primitive.ObjectIDFromHex(censusID)
	c.Assert(err, qt.IsNil)

	return census
}

func TestProcess(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("TestSetAndGetProcess", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// test not found process
		process, err := testDB.Process(testProcessID)
		c.Assert(process, qt.IsNil)
		c.Assert(err, qt.Not(qt.IsNil))
		var rootHex internal.HexBytes
		if err := rootHex.ParseString(testProcessRoot); err != nil {
			c.Assert(err, qt.Not(qt.IsNil))
		}

		census := &Census{
			ID:         primitive.NewObjectID(),
			OrgAddress: testNonExistentOrg,
			Type:       CensusTypeMail,
			Published: PublishedCensus{
				URI:  testProcessURI,
				Root: rootHex,
			},
		}

		// Test with non-existent organization
		nonExistentProcess := &Process{
			ID:         testProcessID,
			OrgAddress: testNonExistentOrg,
			Census:     *census,
		}
		err = testDB.SetProcess(nonExistentProcess)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "failed to get organization")

		// Setup prerequisites
		census = setupTestPrerequisites1(c, testDB)

		// create a new process
		process = &Process{
			ID:         testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *census,
			Metadata:   testProcessMetadata,
		}

		// test setting the process
		err = testDB.SetProcess(process)
		c.Assert(err, qt.IsNil)

		// test retrieving the process
		retrieved, err := testDB.Process(testProcessID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrieved, qt.Not(qt.IsNil))
		c.Assert(retrieved.ID, qt.DeepEquals, testProcessID)
		c.Assert(retrieved.OrgAddress, qt.Equals, testOrgAddress)
		c.Assert(retrieved.Census.Published.URI, qt.Equals, testProcessURI)
		c.Assert(retrieved.Census.Published.Root, qt.DeepEquals, rootHex)
		c.Assert(retrieved.Census.ID, qt.Equals, census.ID)
		c.Assert(retrieved.Metadata, qt.DeepEquals, testProcessMetadata)
	})

	t.Run("TestSetProcessValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// test with empty ID
		invalidProcess := &Process{
			OrgAddress: testOrgAddress,
			Census:     *census,
		}
		err := testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// test with empty OrgAddress
		invalidProcess = &Process{
			ID:     testProcessID,
			Census: *census,
		}
		err = testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// test with empty Census Published Root
		nonPublishedCensus := &Census{
			OrgAddress: testOrgAddress,
			Type:       CensusTypeMail,
			Published: PublishedCensus{
				URI: testProcessURI,
			},
		}
		nonPublishedCensusID, err := testDB.SetCensus(nonPublishedCensus)
		c.Assert(err, qt.IsNil)
		nonPublishedCensus.ID, err = primitive.ObjectIDFromHex(nonPublishedCensusID)
		c.Assert(err, qt.IsNil)
		invalidProcess = &Process{
			ID:         testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *nonPublishedCensus,
		}
		err = testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.IsNotNil)
		c.Assert(err.Error(), qt.Contains, "does not have a published root or URI")
	})

	t.Run("TestDeleteProcess", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// create a process
		process := &Process{
			ID:         testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *census,
		}
		err := testDB.SetProcess(process)
		c.Assert(err, qt.IsNil)

		// test deleting the process
		err = testDB.DelProcess(testProcessID)
		c.Assert(err, qt.IsNil)

		// verify it's deleted
		retrieved, err := testDB.Process(testProcessID)
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Not(qt.IsNil))

		// test delete with empty ID
		var emptyID internal.HexBytes
		err = testDB.DelProcess(emptyID)
		c.Assert(err, qt.Equals, ErrInvalidData)
	})
}
