package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	testProcessID       = internal.HexBytes("test_process_id")
	testProcessRoot     = "0xabcde"
	testProcessURI      = "test_process_uri"
	testProcessMetadata = map[string]any{"key1": "value1", "key2": "value2"}
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
		_ = setupTestPrerequisites1(c, testDB)
		// test not found process
		process, err := testDB.ProcessByAddress(testProcessID)
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
			Address:    testProcessID,
			OrgAddress: testNonExistentOrg,
			Census:     *census,
		}
		_, err = testDB.SetProcess(nonExistentProcess)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "failed to get organization")

		// Setup prerequisites
		census = setupTestPrerequisites1(c, testDB)

		// create a new process
		process = &Process{
			Address:    testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *census,
			Metadata:   testProcessMetadata,
		}

		// test setting the process
		pid, err := testDB.SetProcess(process)
		c.Assert(err, qt.IsNil)

		// test retrieving the process
		retrieved, err := testDB.Process(pid)
		c.Assert(err, qt.IsNil)
		c.Assert(retrieved, qt.Not(qt.IsNil))
		c.Assert(retrieved.ID, qt.Not(qt.Equals), primitive.NilObjectID)
		c.Assert(retrieved.OrgAddress, qt.Equals, testOrgAddress)
		c.Assert(retrieved.Census.Published.URI, qt.Equals, testProcessURI)
		c.Assert(retrieved.Census.Published.Root, qt.DeepEquals, rootHex)
		c.Assert(retrieved.Census.ID, qt.Equals, census.ID)
		c.Assert(retrieved.Metadata, qt.DeepEquals, testProcessMetadata)
		c.Assert(retrieved.Address, qt.DeepEquals, testProcessID)

		// update a process
		retrieved.Metadata["key1"] = "newvalue1"
		_, err = testDB.SetProcess(retrieved)
		c.Assert(err, qt.IsNil)

		// retrieve updated process
		updated, err := testDB.Process(pid)
		c.Assert(err, qt.IsNil)
		c.Assert(updated.Metadata["key1"], qt.Equals, "newvalue1")
	})

	t.Run("TestSetProcessValidation", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		_ = setupTestPrerequisites1(c, testDB)
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
		invalidProcess := &Process{
			Address:    testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *nonPublishedCensus,
		}
		_, err = testDB.SetProcess(invalidProcess)
		c.Assert(err, qt.IsNotNil)
		c.Assert(err.Error(), qt.Contains, "does not have a published root or URI")
	})

	t.Run("TestDeleteProcess", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// create a process
		process := &Process{
			Address:    testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *census,
		}
		pid, err := testDB.SetProcess(process)
		c.Assert(err, qt.IsNil)

		// test deleting the process
		err = testDB.DelProcess(pid)
		c.Assert(err, qt.IsNil)

		// verify it's deleted
		retrieved, err := testDB.ProcessByAddress(testProcessID)
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Not(qt.IsNil))

		// test delete with empty ID
		err = testDB.DelProcess(primitive.NilObjectID)
		c.Assert(err, qt.Equals, ErrInvalidData)
	})

	t.Run("TestListProcesses", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		census := setupTestPrerequisites1(c, testDB)

		// Create draft process (no address)
		draftProcess := &Process{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Metadata:   map[string]any{"type": "draft"},
		}
		draftID, err := testDB.SetProcess(draftProcess)
		c.Assert(err, qt.IsNil)

		// Create published process (with address)
		publishedProcess := &Process{
			Address:    testProcessID,
			OrgAddress: testOrgAddress,
			Census:     *census,
			Metadata:   map[string]any{"type": "published"},
		}
		publishedID, err := testDB.SetProcess(publishedProcess)
		c.Assert(err, qt.IsNil)

		// Test listing draft processes
		totalItems, processes, err := testDB.ListProcesses(testOrgAddress, 1, 10, DraftOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(totalItems, qt.Equals, int64(1))
		c.Assert(processes, qt.HasLen, 1)
		c.Assert(processes[0].ID, qt.Equals, draftID)
		c.Assert(processes[0].Address, qt.IsNil)
		c.Assert(processes[0].Metadata["type"], qt.Equals, "draft")

		// Test listing published processes
		totalItems, processes, err = testDB.ListProcesses(testOrgAddress, 1, 10, PublishedOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(totalItems, qt.Equals, int64(1))
		c.Assert(processes, qt.HasLen, 1)
		c.Assert(processes[0].ID, qt.Equals, publishedID)
		c.Assert(processes[0].Address, qt.DeepEquals, testProcessID)
		c.Assert(processes[0].Metadata["type"], qt.Equals, "published")

		// Test with non-existent organization
		totalItems, processes, err = testDB.ListProcesses(testNonExistentOrg, 1, 10, AllProcesses)
		c.Assert(err, qt.IsNil)
		c.Assert(totalItems, qt.Equals, int64(0))
		c.Assert(processes, qt.HasLen, 0)

		// Test with empty organization address
		totalItems, processes, err = testDB.ListProcesses(common.Address{}, 1, 10, AllProcesses)
		c.Assert(err, qt.Equals, ErrInvalidData)
		c.Assert(totalItems, qt.Equals, int64(0))
		c.Assert(processes, qt.IsNil)

		// Test pagination
		// Create more draft processes
		for i := 0; i < 5; i++ {
			extraDraftProcess := &Process{
				OrgAddress: testOrgAddress,
				Census:     *census,
				Metadata:   map[string]any{"type": "draft", "index": i},
			}
			_, err = testDB.SetProcess(extraDraftProcess)
			c.Assert(err, qt.IsNil)
		}

		// Test first page with page size 3
		totalItems, processes, err = testDB.ListProcesses(testOrgAddress, 1, 3, DraftOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(totalItems, qt.Equals, int64(6)) // 6 draft processes total, 3 per page = 2 pages
		c.Assert(processes, qt.HasLen, 3)

		// Test second page
		totalItems, processes, err = testDB.ListProcesses(testOrgAddress, 2, 3, DraftOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(totalItems, qt.Equals, int64(6))
		c.Assert(processes, qt.HasLen, 3)
	})
}
