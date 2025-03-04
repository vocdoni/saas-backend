package db

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	testProcessID2 = internal.HexBytes("test_process_id_2")
	testProcessID3 = internal.HexBytes("test_process_id_3")
)

// Helper function to create a test process
func createTestProcess(c *qt.C, processID internal.HexBytes, publishedCensus *PublishedCensus) *Process {
	process := &Process{
		ID:              processID,
		OrgAddress:      testOrgAddress,
		PublishedCensus: *publishedCensus,
		Metadata:        testProcessMetadata,
	}
	err := db.SetProcess(process)
	c.Assert(err, qt.IsNil)
	return process
}

func TestSetAndGetProcessBundle(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// Create test processes
	process1 := createTestProcess(c, testProcessID, publishedCensus)
	process2 := createTestProcess(c, testProcessID2, publishedCensus)

	// Test with empty processes array - should be valid now
	emptyBundle := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{},
	}
	emptyBundleID, err := db.SetProcessBundle(emptyBundle)
	c.Assert(err, qt.IsNil)
	c.Assert(emptyBundleID, qt.Not(qt.Equals), "")

	// Test with non-existent organization
	nonExistentBundle := &ProcessesBundle{
		OrgAddress: "non-existent-org",
		Census: Census{
			ID:         primitive.NewObjectID(),
			OrgAddress: "non-existent-org",
			Type:       CensusTypeMail,
		},
		CensusRoot: testProcessRoot,
		Processes:  []internal.HexBytes{testProcessID3},
	}
	_, err = db.SetProcessBundle(nonExistentBundle)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Contains, "failed to get organization")

	// Create a new process bundle
	bundle := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process1.ID, process2.ID},
	}
	bundleID, err := db.SetProcessBundle(bundle)
	c.Assert(err, qt.IsNil)
	c.Assert(bundleID, qt.Not(qt.Equals), "")

	// Convert the bundle ID to ObjectID
	objID, err := primitive.ObjectIDFromHex(bundleID)
	c.Assert(err, qt.IsNil)

	// Test retrieving the process bundle
	retrieved, err := db.ProcessBundle(objID)
	c.Assert(err, qt.IsNil)
	c.Assert(retrieved, qt.Not(qt.IsNil))
	c.Assert(retrieved.ID, qt.Equals, objID)
	c.Assert(retrieved.Processes, qt.HasLen, 2)
	c.Assert(retrieved.Processes[0], qt.DeepEquals, process1.ID)
	c.Assert(retrieved.Processes[1], qt.DeepEquals, process2.ID)

	// Test updating an existing bundle
	process3 := createTestProcess(c, testProcessID3, publishedCensus)
	retrieved.Processes = append(retrieved.Processes, process3.ID)
	updatedBundleID, err := db.SetProcessBundle(retrieved)
	c.Assert(err, qt.IsNil)
	c.Assert(updatedBundleID, qt.Equals, bundleID)

	// Verify the update
	updated, err := db.ProcessBundle(objID)
	c.Assert(err, qt.IsNil)
	c.Assert(updated, qt.Not(qt.IsNil))
	c.Assert(updated.Processes, qt.HasLen, 3)
	c.Assert(updated.Processes[2], qt.DeepEquals, process3.ID)
}

func TestProcessBundles(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// Create test processes
	process1 := createTestProcess(c, testProcessID, publishedCensus)
	process2 := createTestProcess(c, testProcessID2, publishedCensus)
	process3 := createTestProcess(c, testProcessID3, publishedCensus)

	// Initially there should be no bundles
	bundles, err := db.ProcessBundles()
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 0)

	// Create two process bundles
	bundle1 := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process1.ID, process2.ID},
	}
	bundle1ID, err := db.SetProcessBundle(bundle1)
	c.Assert(err, qt.IsNil)

	bundle2 := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process2.ID, process3.ID},
	}
	bundle2ID, err := db.SetProcessBundle(bundle2)
	c.Assert(err, qt.IsNil)

	// Test retrieving all process bundles
	bundles, err = db.ProcessBundles()
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 2)

	// Verify the bundle IDs match
	bundleIDs := []string{bundles[0].ID.Hex(), bundles[1].ID.Hex()}
	c.Assert(bundleIDs, qt.Contains, bundle1ID)
	c.Assert(bundleIDs, qt.Contains, bundle2ID)
}

func TestProcessBundlesByProcess(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// Create test processes
	process1 := createTestProcess(c, testProcessID, publishedCensus)
	process2 := createTestProcess(c, testProcessID2, publishedCensus)
	process3 := createTestProcess(c, testProcessID3, publishedCensus)

	// Create two process bundles
	bundle1 := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process1.ID, process2.ID},
	}
	bundle1ID, err := db.SetProcessBundle(bundle1)
	c.Assert(err, qt.IsNil)

	bundle2 := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process2.ID, process3.ID},
	}
	bundle2ID, err := db.SetProcessBundle(bundle2)
	c.Assert(err, qt.IsNil)

	// Test with invalid process ID
	var emptyID internal.HexBytes
	_, err = db.ProcessBundlesByProcess(emptyID)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test retrieving bundles by process ID
	bundles, err := db.ProcessBundlesByProcess(testProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 1)
	c.Assert(bundles[0].ID.Hex(), qt.Equals, bundle1ID)

	bundles, err = db.ProcessBundlesByProcess(testProcessID2)
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 2)
	bundleIDs := []string{bundles[0].ID.Hex(), bundles[1].ID.Hex()}
	c.Assert(bundleIDs, qt.Contains, bundle1ID)
	c.Assert(bundleIDs, qt.Contains, bundle2ID)

	bundles, err = db.ProcessBundlesByProcess(testProcessID3)
	c.Assert(err, qt.IsNil)
	c.Assert(bundles, qt.HasLen, 1)
	c.Assert(bundles[0].ID.Hex(), qt.Equals, bundle2ID)
}

func TestAddProcessesToBundle(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// Create test processes
	process1 := createTestProcess(c, testProcessID, publishedCensus)
	process2 := createTestProcess(c, testProcessID2, publishedCensus)
	process3 := createTestProcess(c, testProcessID3, publishedCensus)

	// Create a process bundle with one process
	bundle := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process1.ID},
	}
	bundleID, err := db.SetProcessBundle(bundle)
	c.Assert(err, qt.IsNil)
	objID, err := primitive.ObjectIDFromHex(bundleID)
	c.Assert(err, qt.IsNil)

	// Test with invalid bundle ID
	err = db.AddProcessesToBundle(primitive.NilObjectID, []internal.HexBytes{process2.ID})
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test with empty processes array
	err = db.AddProcessesToBundle(objID, []internal.HexBytes{})
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test with non-existent process ID (should not error as process existence is not validated)
	nonExistentProcessID := internal.HexBytes("non-existent-process")
	err = db.AddProcessesToBundle(objID, []internal.HexBytes{nonExistentProcessID})
	c.Assert(err, qt.IsNil)

	// Add processes to the bundle
	err = db.AddProcessesToBundle(objID, []internal.HexBytes{process2.ID, process3.ID})
	c.Assert(err, qt.IsNil)

	// Verify the processes were added
	retrieved, err := db.ProcessBundle(objID)
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
	err = db.AddProcessesToBundle(objID, []internal.HexBytes{process2.ID})
	c.Assert(err, qt.IsNil)

	// Verify no duplication occurred
	retrieved, err = db.ProcessBundle(objID)
	c.Assert(err, qt.IsNil)
	c.Assert(retrieved, qt.Not(qt.IsNil))
	c.Assert(retrieved.Processes, qt.HasLen, 4) // Still 4 processes, no duplication
}

func TestDelProcessBundle(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	// Setup prerequisites
	publishedCensus := setupTestPrerequisites1(c)

	// Create test processes
	process1 := createTestProcess(c, testProcessID, publishedCensus)
	process2 := createTestProcess(c, testProcessID2, publishedCensus)

	// Create a process bundle
	bundle := &ProcessesBundle{
		OrgAddress: testOrgAddress,
		Census:     publishedCensus.Census,
		CensusRoot: publishedCensus.Root,
		Processes:  []internal.HexBytes{process1.ID, process2.ID},
	}
	bundleID, err := db.SetProcessBundle(bundle)
	c.Assert(err, qt.IsNil)
	objID, err := primitive.ObjectIDFromHex(bundleID)
	c.Assert(err, qt.IsNil)

	// Test with invalid bundle ID
	err = db.DelProcessBundle(primitive.NilObjectID)
	c.Assert(err, qt.Equals, ErrInvalidData)

	// Test deleting the bundle
	err = db.DelProcessBundle(objID)
	c.Assert(err, qt.IsNil)

	// Verify the bundle is deleted
	retrieved, err := db.ProcessBundle(objID)
	c.Assert(retrieved, qt.IsNil)
	c.Assert(err, qt.Not(qt.IsNil))

	// Test deleting a non-existent bundle
	err = db.DelProcessBundle(objID)
	c.Assert(err, qt.Equals, ErrNotFound)
}
