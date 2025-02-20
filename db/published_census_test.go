package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	testURI  = "test_uri"
	testRoot = []byte("test_root")
)

func setupTestOrganization(c *qt.C) {
	org := &Organization{
		Address:   "test_org",
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)
}

func TestPublishedCensus(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// test not found census
	census, err := db.PublishedCensus(testRoot, testURI)
	c.Assert(census, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)

	// Create organization first
	setupTestOrganization(c)

	// create test census data
	testCensus := Census{
		OrgAddress: "test_org",
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	// set census
	censusOID, err := db.SetCensus(&testCensus)
	c.Assert(err, qt.IsNil)
	testCensus.ID, err = primitive.ObjectIDFromHex(censusOID)
	c.Assert(err, qt.IsNil)

	// create a new published census
	publishedCensus := &PublishedCensus{
		URI:    testURI,
		Root:   testRoot,
		Census: testCensus,
	}

	// test setting the published census
	err = db.SetPublishedCensus(publishedCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(publishedCensus.CreatedAt.IsZero(), qt.IsFalse)

	// test retrieving the published census
	retrieved, err := db.PublishedCensus(testRoot, testURI)
	c.Assert(err, qt.IsNil)
	c.Assert(retrieved, qt.Not(qt.IsNil))
	c.Assert(retrieved.URI, qt.Equals, testURI)
	c.Assert(retrieved.Root, qt.DeepEquals, testRoot)
	c.Assert(retrieved.Census.ID, qt.Equals, testCensus.ID)
	c.Assert(retrieved.Census.OrgAddress, qt.Equals, testCensus.OrgAddress)
	c.Assert(retrieved.Census.Type, qt.Equals, testCensus.Type)
	c.Assert(retrieved.CreatedAt.IsZero(), qt.IsFalse)

	// Test updating an existing published census
	time.Sleep(time.Millisecond) // Ensure different UpdatedAt timestamp
	err = db.SetPublishedCensus(publishedCensus)
	c.Assert(err, qt.IsNil)

	// Verify the published census was updated correctly
	updatedCensus, err := db.PublishedCensus(testRoot, testURI)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedCensus.CreatedAt, qt.Equals, retrieved.CreatedAt)
}

func TestSetPublishedCensusInvalid(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// Create organization first
	setupTestOrganization(c)

	// test with empty URI
	invalidCensus := &PublishedCensus{
		Root: testRoot,
		Census: Census{
			ID: primitive.NewObjectID(),
		},
	}
	err := db.SetPublishedCensus(invalidCensus)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test with empty Root
	invalidCensus = &PublishedCensus{
		URI: testURI,
		Census: Census{
			ID: primitive.NewObjectID(),
		},
	}
	err = db.SetPublishedCensus(invalidCensus)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test with nil Census ID
	invalidCensus = &PublishedCensus{
		URI:  testURI,
		Root: testRoot,
		Census: Census{
			ID: primitive.NilObjectID,
		},
	}
	err = db.SetPublishedCensus(invalidCensus)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test with non-existent Census ID
	invalidCensus = &PublishedCensus{
		URI:  testURI,
		Root: testRoot,
		Census: Census{
			ID: primitive.NewObjectID(),
		},
	}
	err = db.SetPublishedCensus(invalidCensus)
	c.Assert(err, qt.Not(qt.IsNil))
}

func TestDelPublishedCensus(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// Create organization first
	setupTestOrganization(c)

	// create test census data
	testCensus := Census{
		OrgAddress: "test_org",
		Type:       CensusTypeMail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	// set census
	censusOID, err := db.SetCensus(&testCensus)
	c.Assert(err, qt.IsNil)
	testCensus.ID, err = primitive.ObjectIDFromHex(censusOID)
	c.Assert(err, qt.IsNil)

	// create a published census
	publishedCensus := &PublishedCensus{
		URI:    testURI,
		Root:   testRoot,
		Census: testCensus,
	}
	err = db.SetPublishedCensus(publishedCensus)
	c.Assert(err, qt.IsNil)

	// test deleting with invalid parameters
	err = db.DelPublishedCensus(nil, testURI)
	c.Assert(err, qt.Equals, ErrInvalidData)

	err = db.DelPublishedCensus(testRoot, "")
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test deleting the published census
	err = db.DelPublishedCensus(testRoot, testURI)
	c.Assert(err, qt.IsNil)

	// verify it's deleted
	retrieved, err := db.PublishedCensus(testRoot, testURI)
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)

	// test deleting non-existent published census (should not error)
	err = db.DelPublishedCensus(testRoot, testURI)
	c.Assert(err, qt.IsNil)
}

func TestPublishedCensusInvalid(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// test get with invalid parameters
	retrieved, err := db.PublishedCensus(nil, testURI)
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Equals, ErrInvalidData)

	retrieved, err = db.PublishedCensus(testRoot, "")
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// test getting non-existent published census
	retrieved, err = db.PublishedCensus(testRoot, testURI)
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Equals, ErrNotFound)
}
