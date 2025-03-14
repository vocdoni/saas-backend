package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestPublishedCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	// Helper function to create a test census
	setupTestCensus := func(t *testing.T) (*Census, string) {
		// Create test organization first
		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
		}
		err := testDB.SetOrganization(org)
		if err != nil {
			t.Fatalf("failed to set organization: %v", err)
		}

		// Create test census
		testCensus := &Census{
			OrgAddress: testOrgAddress,
			Type:       CensusTypeMail,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}

		// Set census
		censusID, err := testDB.SetCensus(testCensus)
		if err != nil {
			t.Fatalf("failed to set census: %v", err)
		}

		testCensus.ID, err = primitive.ObjectIDFromHex(censusID)
		if err != nil {
			t.Fatalf("failed to convert census ID: %v", err)
		}

		return testCensus, censusID
	}

	t.Run("GetPublishedCensus", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test not found census
		census, err := testDB.PublishedCensus(testRoot, testURI, primitive.NewObjectID().Hex())
		c.Assert(census, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)

		// Create test census
		testCensus, _ := setupTestCensus(t)

		// Create a new published census
		publishedCensus := &PublishedCensus{
			URI:    testURI,
			Root:   testRoot,
			Census: *testCensus,
		}

		// Test setting the published census
		err = testDB.SetPublishedCensus(publishedCensus)
		c.Assert(err, qt.IsNil)
		c.Assert(publishedCensus.CreatedAt.IsZero(), qt.IsFalse)

		// Test retrieving the published census
		retrieved, err := testDB.PublishedCensus(testRoot, testURI, publishedCensus.Census.ID.Hex())
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
		err = testDB.SetPublishedCensus(publishedCensus)
		c.Assert(err, qt.IsNil)

		// Verify the published census was updated correctly
		updatedCensus, err := testDB.PublishedCensus(testRoot, testURI, publishedCensus.Census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(updatedCensus.CreatedAt, qt.Equals, retrieved.CreatedAt)
	})

	t.Run("SetPublishedCensusInvalid", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test with empty URI
		invalidCensus := &PublishedCensus{
			Root: testRoot,
			Census: Census{
				ID: primitive.NewObjectID(),
			},
		}
		err := testDB.SetPublishedCensus(invalidCensus)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with empty Root
		invalidCensus = &PublishedCensus{
			URI: testURI,
			Census: Census{
				ID: primitive.NewObjectID(),
			},
		}
		err = testDB.SetPublishedCensus(invalidCensus)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with nil Census ID
		invalidCensus = &PublishedCensus{
			URI:  testURI,
			Root: testRoot,
			Census: Census{
				ID: primitive.NilObjectID,
			},
		}
		err = testDB.SetPublishedCensus(invalidCensus)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with non-existent Census ID
		invalidCensus = &PublishedCensus{
			URI:  testURI,
			Root: testRoot,
			Census: Census{
				ID: primitive.NewObjectID(),
			},
		}
		err = testDB.SetPublishedCensus(invalidCensus)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("DelPublishedCensus", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Create test census
		testCensus, _ := setupTestCensus(t)

		// Create a published census
		publishedCensus := &PublishedCensus{
			URI:    testURI,
			Root:   testRoot,
			Census: *testCensus,
		}
		err := testDB.SetPublishedCensus(publishedCensus)
		c.Assert(err, qt.IsNil)

		// Test deleting with invalid parameters
		err = testDB.DelPublishedCensus("", testURI)
		c.Assert(err, qt.Equals, ErrInvalidData)

		err = testDB.DelPublishedCensus(testRoot, "")
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test deleting the published census
		err = testDB.DelPublishedCensus(testRoot, testURI)
		c.Assert(err, qt.IsNil)

		// Verify it's deleted
		retrieved, err := testDB.PublishedCensus(testRoot, testURI, publishedCensus.Census.ID.Hex())
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)

		// Test deleting non-existent published census (should not error)
		err = testDB.DelPublishedCensus(testRoot, testURI)
		c.Assert(err, qt.IsNil)
	})

	t.Run("PublishedCensusInvalid", func(t *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Test get with invalid parameters
		retrieved, err := testDB.PublishedCensus("nil", "nil", "nil")
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Equals, ErrInvalidData)

		retrieved, err = testDB.PublishedCensus(testRoot, "", "")
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test getting non-existent published census
		retrieved, err = testDB.PublishedCensus(testRoot, testURI, primitive.NewObjectID().Hex())
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Equals, ErrNotFound)
	})
}
