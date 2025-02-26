package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	testProcessID       = []byte("test_process_id")
	testProcessRoot     = "test_process_root"
	testProcessURI      = "test_process_uri"
	testProcessMetadata = []byte("test_metadata")
)

func setupTestPrerequisites1(c *qt.C) *PublishedCensus {
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

func TestSetAndGetProcess(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// test not found process
	process, err := db.Process(testProcessID)
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
	err = db.SetProcess(nonExistentProcess)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Contains, "failed to get organization")

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// create a new process
	process = &Process{
		ID:              testProcessID,
		OrgAddress:      testOrgAddress,
		PublishedCensus: *publishedCensus,
		Metadata:        testProcessMetadata,
	}

	// test setting the process
	err = db.SetProcess(process)
	c.Assert(err, qt.IsNil)

	// test retrieving the process
	retrieved, err := db.Process(testProcessID)
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
		ID:              []byte("new-process"),
		OrgAddress:      testOrgAddress,
		PublishedCensus: newPublishedCensus,
	}
	err = db.SetProcess(newProcess)
	c.Assert(err, qt.IsNil)

	// Verify the published census was created
	createdPublishedCensus, err := db.PublishedCensus("new-root", "new-uri")
	c.Assert(err, qt.IsNil)
	c.Assert(createdPublishedCensus, qt.Not(qt.IsNil))
}

func TestSetProcessValidation(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// test with empty ID
	invalidProcess := &Process{
		OrgAddress:      testOrgAddress,
		PublishedCensus: *publishedCensus,
	}
	err := db.SetProcess(invalidProcess)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test with empty OrgAddress
	invalidProcess = &Process{
		ID:              testProcessID,
		PublishedCensus: *publishedCensus,
	}
	err = db.SetProcess(invalidProcess)
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
	err = db.SetProcess(invalidProcess)
	c.Assert(err, qt.Equals, ErrInvalidData)
}

func TestDeleteProcess(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// create a process
	process := &Process{
		ID:              testProcessID,
		OrgAddress:      testOrgAddress,
		PublishedCensus: *publishedCensus,
	}
	err := db.SetProcess(process)
	c.Assert(err, qt.IsNil)

	// test deleting the process
	err = db.DelProcess(testProcessID)
	c.Assert(err, qt.IsNil)

	// verify it's deleted
	retrieved, err := db.Process(testProcessID)
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Not(qt.IsNil))

	// test delete with empty ID
	err = db.DelProcess(nil)
	c.Assert(err, qt.Equals, ErrInvalidData)
}
