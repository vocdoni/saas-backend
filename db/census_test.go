package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	testOrgAddress = "0x123456789"
)

func TestSetCensus(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// Test with non-existent organization
	nonExistentCensus := &Census{
		OrgAddress: "non-existent-org",
		Type:       CensusTypeMail,
	}
	_, err := db.SetCensus(nonExistentCensus)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Contains, "organization not found")

	// Create test organization first
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err = db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Test with invalid data
	invalidCensus := &Census{
		OrgAddress: "",
		Type:       CensusTypeMail,
	}
	_, err = db.SetCensus(invalidCensus)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test creating a new census
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
	}

	// Create new census
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)
	c.Assert(censusID, qt.Not(qt.Equals), "")

	// Verify the census was created correctly
	createdCensus, err := db.Census(censusID)
	c.Assert(err, qt.IsNil)
	c.Assert(createdCensus.OrgAddress, qt.Equals, testOrgAddress)
	c.Assert(createdCensus.Type, qt.Equals, CensusTypeMail)
	c.Assert(createdCensus.CreatedAt.IsZero(), qt.IsFalse)

	// Test updating an existing census
	createdCensus.Type = CensusTypeSMS

	// Ensure different UpdatedAt timestamp
	time.Sleep(time.Millisecond)

	// Update census
	updatedID, err := db.SetCensus(createdCensus)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedID, qt.Equals, censusID)

	// Verify the census was updated correctly
	updatedCensus, err := db.Census(updatedID)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedCensus.Type, qt.Equals, CensusTypeSMS)
	c.Assert(updatedCensus.CreatedAt, qt.Equals, createdCensus.CreatedAt)
	c.Assert(updatedCensus.UpdatedAt.After(createdCensus.CreatedAt), qt.IsTrue)
}

func TestDelCensus(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// Create test organization first
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Create a census to delete
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
	}

	// Create new census
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// Test deleting with invalid ID
	err = db.DelCensus("")
	c.Assert(err, qt.Equals, ErrInvalidData)

	err = db.DelCensus("invalid-id")
	c.Assert(err, qt.Not(qt.IsNil))

	// Test deleting with valid ID
	err = db.DelCensus(censusID)
	c.Assert(err, qt.IsNil)

	// Verify the census was deleted
	_, err = db.Census(censusID)
	c.Assert(err, qt.Not(qt.IsNil))
}

func TestCensus(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := db.Reset(); err != nil {
			t.Error(err)
		}
	}()

	// Create test organization first
	org := &Organization{
		Address:   testOrgAddress,
		Active:    true,
		CreatedAt: time.Now(),
	}
	err := db.SetOrganization(org)
	c.Assert(err, qt.IsNil)

	// Test getting census with invalid ID
	_, err = db.Census("")
	c.Assert(err, qt.Equals, ErrInvalidData)

	_, err = db.Census("invalid-id")
	c.Assert(err, qt.Not(qt.IsNil))

	// Create a census to retrieve
	census := &Census{
		OrgAddress: testOrgAddress,
		Type:       CensusTypeMail,
	}

	// Create new census
	censusID, err := db.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// Test getting census with valid ID
	retrievedCensus, err := db.Census(censusID)
	c.Assert(err, qt.IsNil)
	c.Assert(retrievedCensus.OrgAddress, qt.Equals, testOrgAddress)
	c.Assert(retrievedCensus.Type, qt.Equals, CensusTypeMail)
	c.Assert(retrievedCensus.CreatedAt.IsZero(), qt.IsFalse)

	// Test getting non-existent census
	nonExistentID := primitive.NewObjectID().Hex()
	_, err = db.Census(nonExistentID)
	c.Assert(err, qt.Not(qt.IsNil))
}
