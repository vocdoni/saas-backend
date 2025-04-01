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
	testProcessRoot     = "test_process_root"
	testProcessURI      = "test_process_uri"
	testProcessMetadata = []byte("test_metadata")
)

func setupTestPrerequisites1(c *qt.C, db *MongoStorage) *PublishedCensus {
	// Create test organization
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Create test census
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)
	census.ID, err = primitive.ObjectIDFromHex(censusID)
	c.Assert(err, qt.IsNil)

	// Create test published census
	publishedCensus := &PublishedCensus{
		URI:       testProcessURI,
		Root:      testProcessRoot,
		Census:    *census,
		CreatedAt: time.Now(),
	}
	err = db.SetPublishedCensus(publishedCensus)
	c.Assert(err, qt.IsNil)

	return publishedCensus
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

		// Test with non-existent organization
		nonExistentProcess := &Process{
			ID:         testProcessID,
			OrgAddress: "non-existent-org",
			PublishedCensus: PublishedCensus{
				URI:  testProcessURI,
				Root: testProcessRoot,
				Census: Census{
					ID:         primitive.NewObjectID(),
					OrgAddress: "non-existent-org",
					Type:       CensusTypeMail,
				},
			},
		}
		err = testDB.SetProcess(nonExistentProcess)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "failed to get organization")

		// Setup prerequisites
		publishedCensus := setupTestPrerequisites1(c, testDB)

		// create a new process
		process = &Process{
			ID:              testProcessID,
			OrgAddress:      testOrgAddress,
			PublishedCensus: *publishedCensus,
			Metadata:        testProcessMetadata,
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
		c.Assert(retrieved.PublishedCensus.URI, qt.Equals, testProcessURI)
		c.Assert(retrieved.PublishedCensus.Root, qt.DeepEquals, testProcessRoot)
		c.Assert(retrieved.PublishedCensus.Census.ID, qt.Equals, publishedCensus.Census.ID)
		c.Assert(retrieved.Metadata, qt.DeepEquals, testProcessMetadata)

		// Test with non-existent published census (should create it)
		newPublishedCensus := PublishedCensus{
			URI:  "new-uri",
			Root: "new-root",
			Census: Census{
				ID:         publishedCensus.Census.ID,
				OrgAddress: testOrgAddress,
				Type:       CensusTypeMail,
			},
		}
		newProcess := &Process{
			ID:              internal.HexBytes("new-process"),
			OrgAddress:      testOrgAddress,
			PublishedCensus: newPublishedCensus,
		}
		err = testDB.SetProcess(newProcess)
		c.Assert(err, qt.IsNil)

		// Verify the published census was created
		createdPublishedCensus, err := testDB.PublishedCensus("new-root", "new-uri", publishedCensus.Census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(createdPublishedCensus, qt.Not(qt.IsNil))
	})

	t.Run("TestSetProcessValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		publishedCensus := setupTestPrerequisites1(c, testDB)

		// test with empty ID
		invalidProcess := &Process{
			OrgAddress:      testOrgAddress,
			PublishedCensus: *publishedCensus,
		}
		err := testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// test with empty OrgAddress
		invalidProcess = &Process{
			ID:              testProcessID,
			PublishedCensus: *publishedCensus,
		}
		err = testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// test with empty PublishedCensus Root
		invalidProcess = &Process{
			ID:         testProcessID,
			OrgAddress: testOrgAddress,
			PublishedCensus: PublishedCensus{
				URI: testProcessURI,
				Census: Census{
					ID: publishedCensus.Census.ID,
				},
			},
		}
		err = testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("TestDeleteProcess", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		publishedCensus := setupTestPrerequisites1(c, testDB)

		// create a process
		process := &Process{
			ID:              testProcessID,
			OrgAddress:      testOrgAddress,
			PublishedCensus: *publishedCensus,
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
