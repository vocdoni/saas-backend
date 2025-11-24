package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	testProcessID2 = internal.HexBytesFromString("0x2222")
	testProcessID3 = internal.HexBytesFromString("0x3333")
)

// Helper function to create a test process
func createTestProcess(c *qt.C, db *MongoStorage, processID internal.HexBytes, census *Census) *Process {
	process := &Process{
		Address:    processID,
		OrgAddress: testOrgAddress,
		Census:     *census,
		Metadata:   testProcessMetadata,
	}
	pid, err := db.SetProcess(process)
	c.Assert(err, qt.IsNil)
	process.ID = pid
	return process
}

func TestProcessBundles(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.Reset(), qt.IsNil) })

	t.Run("TestSetAndGetProcessBundle", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// Create test processes
		process1 := createTestProcess(c, testDB, testProcessID, census)
		process2 := createTestProcess(c, testDB, testProcessID2, census)

		// Test with empty processes array - should be valid now
		emptyBundle := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{},
		}
		emptyBundleID, err := testDB.SetProcessBundle(emptyBundle)
		c.Assert(err, qt.IsNil)
		c.Assert(emptyBundleID, qt.Not(qt.Equals), "")

		var rootHex internal.HexBytes
		if err := rootHex.ParseString(testProcessRoot); err != nil {
			c.Assert(err, qt.Not(qt.IsNil))
		}

		// Test with non-existent organization
		nonExistentBundle := &ProcessesBundle{
			OrgAddress: testNonExistentOrg,
			Census: Census{
				ID:         internal.NewObjectID(),
				OrgAddress: testNonExistentOrg,
				Type:       CensusTypeMail,
				Published: PublishedCensus{
					Root: rootHex,
				},
			},
			Processes: []internal.HexBytes{testProcessID3},
		}
		_, err = testDB.SetProcessBundle(nonExistentBundle)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "failed to get organization")

		// Create a new process bundle
		bundle := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process1.Address, process2.Address},
		}
		bundleID, err := testDB.SetProcessBundle(bundle)
		c.Assert(err, qt.IsNil)
		c.Assert(bundleID, qt.Not(qt.Equals), "")

		// Test retrieving the process bundle
		retrieved, err := testDB.ProcessBundle(bundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrieved, qt.Not(qt.IsNil))
		c.Assert(retrieved.ID, qt.Equals, bundleID)
		c.Assert(retrieved.Processes, qt.HasLen, 2)
		c.Assert(retrieved.Processes[0], qt.DeepEquals, process1.Address)
		c.Assert(retrieved.Processes[1], qt.DeepEquals, process2.Address)

		// Test updating an existing bundle
		process3 := createTestProcess(c, testDB, testProcessID3, census)
		retrieved.Processes = append(retrieved.Processes, process3.Address)
		updatedBundleID, err := testDB.SetProcessBundle(retrieved)
		c.Assert(err, qt.IsNil)
		c.Assert(updatedBundleID, qt.DeepEquals, bundleID)

		// Verify the update
		updated, err := testDB.ProcessBundle(bundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(updated, qt.Not(qt.IsNil))
		c.Assert(updated.Processes, qt.HasLen, 3)
		c.Assert(updated.Processes[2], qt.DeepEquals, process3.Address)
	})

	t.Run("TestProcessBundlesList", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// Create test processes
		process1 := createTestProcess(c, testDB, testProcessID, census)
		process2 := createTestProcess(c, testDB, testProcessID2, census)
		process3 := createTestProcess(c, testDB, testProcessID3, census)

		// Initially there should be no bundles
		bundles, err := testDB.ProcessBundles()
		c.Assert(err, qt.IsNil)
		c.Assert(bundles, qt.HasLen, 0)

		// Create two process bundles
		bundle1 := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process1.Address, process2.Address},
		}
		bundle1ID, err := testDB.SetProcessBundle(bundle1)
		c.Assert(err, qt.IsNil)

		bundle2 := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process2.Address, process3.Address},
		}
		bundle2ID, err := testDB.SetProcessBundle(bundle2)
		c.Assert(err, qt.IsNil)

		// Test retrieving all process bundles
		bundles, err = testDB.ProcessBundles()
		c.Assert(err, qt.IsNil)
		c.Assert(bundles, qt.HasLen, 2)

		// Verify the bundle IDs match
		bundleIDs := []internal.ObjectID{bundles[0].ID, bundles[1].ID}
		c.Assert(bundleIDs, qt.Contains, bundle1ID)
		c.Assert(bundleIDs, qt.Contains, bundle2ID)
	})

	t.Run("TestProcessBundlesByProcess", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// Create test processes
		process1 := createTestProcess(c, testDB, testProcessID, census)
		process2 := createTestProcess(c, testDB, testProcessID2, census)
		process3 := createTestProcess(c, testDB, testProcessID3, census)

		// Create two process bundles
		bundle1 := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process1.Address, process2.Address},
		}
		bundle1ID, err := testDB.SetProcessBundle(bundle1)
		c.Assert(err, qt.IsNil)

		bundle2 := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process2.Address, process3.Address},
		}
		bundle2ID, err := testDB.SetProcessBundle(bundle2)
		c.Assert(err, qt.IsNil)

		// Test with invalid process ID
		var emptyID internal.HexBytes
		_, err = testDB.ProcessBundlesByProcess(emptyID)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test retrieving bundles by process ID
		bundles, err := testDB.ProcessBundlesByProcess(testProcessID)
		c.Assert(err, qt.IsNil)
		c.Assert(bundles, qt.HasLen, 1)
		c.Assert(bundles[0].ID, qt.Equals, bundle1ID)

		bundles, err = testDB.ProcessBundlesByProcess(testProcessID2)
		c.Assert(err, qt.IsNil)
		c.Assert(bundles, qt.HasLen, 2)
		bundleIDs := []internal.ObjectID{bundles[0].ID, bundles[1].ID}
		c.Assert(bundleIDs, qt.Contains, bundle1ID)
		c.Assert(bundleIDs, qt.Contains, bundle2ID)

		bundles, err = testDB.ProcessBundlesByProcess(testProcessID3)
		c.Assert(err, qt.IsNil)
		c.Assert(bundles, qt.HasLen, 1)
		c.Assert(bundles[0].ID, qt.Equals, bundle2ID)
	})

	t.Run("TestAddProcessesToBundle", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// Create test processes
		process1 := createTestProcess(c, testDB, testProcessID, census)
		process2 := createTestProcess(c, testDB, testProcessID2, census)
		process3 := createTestProcess(c, testDB, testProcessID3, census)

		// Create a process bundle with one process
		bundle := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process1.Address},
		}
		bundleID, err := testDB.SetProcessBundle(bundle)
		c.Assert(err, qt.IsNil)

		// Test with invalid bundle ID
		err = testDB.AddProcessesToBundle(internal.NilObjectID, []internal.HexBytes{process2.Address})
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with empty processes array
		err = testDB.AddProcessesToBundle(bundleID, []internal.HexBytes{})
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test with non-existent process ID (should not error as process existence is not validated)
		nonExistentProcessID := internal.HexBytes("non-existent-process")
		err = testDB.AddProcessesToBundle(bundleID, []internal.HexBytes{nonExistentProcessID})
		c.Assert(err, qt.IsNil)

		// Add processes to the bundle
		err = testDB.AddProcessesToBundle(bundleID, []internal.HexBytes{process2.Address, process3.Address})
		c.Assert(err, qt.IsNil)

		// Verify the processes were added
		retrieved, err := testDB.ProcessBundle(bundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrieved, qt.Not(qt.IsNil))
		c.Assert(retrieved.Processes, qt.HasLen, 4) // 1 original + 1 non-existent + 2 added

		// Convert processes to strings for easier assertion
		processIDs := make([]string, len(retrieved.Processes))
		for i, p := range retrieved.Processes {
			processIDs[i] = string(p)
		}

		// Check that all expected processes are in the bundle
		c.Assert(processIDs, qt.Contains, string(testProcessID))
		c.Assert(processIDs, qt.Contains, string(nonExistentProcessID))
		c.Assert(processIDs, qt.Contains, string(testProcessID2))
		c.Assert(processIDs, qt.Contains, string(testProcessID3))

		// Test adding a process that already exists in the bundle (should not duplicate)
		err = testDB.AddProcessesToBundle(bundleID, []internal.HexBytes{process2.Address})
		c.Assert(err, qt.IsNil)

		// Verify no duplication occurred
		retrieved, err = testDB.ProcessBundle(bundleID)
		c.Assert(err, qt.IsNil)
		c.Assert(retrieved, qt.Not(qt.IsNil))
		c.Assert(retrieved.Processes, qt.HasLen, 4) // Still 4 processes, no duplication
	})

	t.Run("TestDelProcessBundle", func(_ *testing.T) {
		c.Assert(testDB.Reset(), qt.IsNil)
		// Setup prerequisites
		census := setupTestPrerequisites1(c, testDB)

		// Create test processes
		process1 := createTestProcess(c, testDB, testProcessID, census)
		process2 := createTestProcess(c, testDB, testProcessID2, census)

		// Create a process bundle
		bundle := &ProcessesBundle{
			OrgAddress: testOrgAddress,
			Census:     *census,
			Processes:  []internal.HexBytes{process1.Address, process2.Address},
		}
		bundleID, err := testDB.SetProcessBundle(bundle)
		c.Assert(err, qt.IsNil)

		// Test with invalid bundle ID
		err = testDB.DelProcessBundle(internal.NilObjectID)
		c.Assert(err, qt.Equals, ErrInvalidData)

		// Test deleting the bundle
		err = testDB.DelProcessBundle(bundleID)
		c.Assert(err, qt.IsNil)

		// Verify the bundle is deleted
		retrieved, err := testDB.ProcessBundle(bundleID)
		c.Assert(retrieved, qt.IsNil)
		c.Assert(err, qt.Not(qt.IsNil))

		// Test deleting a non-existent bundle
		err = testDB.DelProcessBundle(bundleID)
		c.Assert(err, qt.Equals, ErrNotFound)
	})
}
