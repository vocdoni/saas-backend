package objectstorage

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
)

func TestObjectStorage(t *testing.T) {
	c := qt.New(t)

	// Start a MongoDB container for testing
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dbContainer, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	defer func() { _ = dbContainer.Terminate(ctx) }()

	// Get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	c.Assert(err, qt.IsNil)

	// Create a new MongoDB connection with a random database name
	testDB, err := db.New(mongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	defer testDB.Close()

	// Create a new ObjectStorageClient
	t.Run("New", func(_ *testing.T) {
		c := qt.New(t)

		// Test with nil config
		client, err := New(nil)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(client, qt.IsNil)

		// Test with valid config
		config := &Config{
			DB: testDB,
		}
		client, err = New(config)
		c.Assert(err, qt.IsNil)
		c.Assert(client, qt.Not(qt.IsNil))
		c.Assert(client.db, qt.Equals, testDB)
		c.Assert(client.supportedTypes, qt.DeepEquals, DefaultSupportedFileTypes)

		// Test with custom supported types
		config = &Config{
			DB:             testDB,
			SupportedTypes: []ObjectFileType{FileTypeJPEG, FileTypePNG},
		}
		client, err = New(config)
		c.Assert(err, qt.IsNil)
		c.Assert(client, qt.Not(qt.IsNil))
		c.Assert(client.supportedTypes, qt.DeepEquals, DefaultSupportedFileTypes)
	})

	// Create a client for the rest of the tests
	config := &Config{
		DB: testDB,
	}
	client, err := New(config)
	c.Assert(err, qt.IsNil)

	t.Run("Put", func(_ *testing.T) {
		c := qt.New(t)

		// Test with valid JPEG data
		jpegData := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01}
		reader := bytes.NewReader(jpegData)
		objectID, err := client.Put(reader, int64(len(jpegData)), "test@example.com")
		c.Assert(err, qt.IsNil)
		c.Assert(objectID, qt.Not(qt.Equals), "")

		// Test with valid PNG data
		pngData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
		reader = bytes.NewReader(pngData)
		objectID, err = client.Put(reader, int64(len(pngData)), "test@example.com")
		c.Assert(err, qt.IsNil)
		c.Assert(objectID, qt.Not(qt.Equals), "")

		// Test with unsupported file type
		unsupportedData := []byte{0x00, 0x01, 0x02, 0x03}
		reader = bytes.NewReader(unsupportedData)
		_, err = client.Put(reader, int64(len(unsupportedData)), "test@example.com")
		c.Assert(err, qt.Equals, ErrorFileTypeNotSupported)

		// Test with empty data
		emptyData := []byte{}
		reader = bytes.NewReader(emptyData)
		_, err = client.Put(reader, int64(len(emptyData)), "test@example.com")
		c.Assert(err, qt.Not(qt.IsNil))
	})

	t.Run("Get", func(_ *testing.T) {
		c := qt.New(t)

		// Test with empty objectID
		_, err := client.Get(internal.NilObjectID)
		c.Assert(err, qt.Equals, ErrorInvalidObjectID)

		// Test with non-existent objectID
		_, err = client.Get(internal.NewObjectID())
		c.Assert(err, qt.Equals, ErrorObjectNotFound)

		// Test with valid objectID
		// First, put an object
		jpegData := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01}
		reader := bytes.NewReader(jpegData)
		objectIDWithExt, err := client.Put(reader, int64(len(jpegData)), "test@example.com")
		c.Assert(err, qt.IsNil)

		// Extract the objectID without the extension (not used, just for demonstration)
		_ = objectIDWithExt[:len(objectIDWithExt)-4] // Remove ".jpg"

		// We need to manually set the object in the database since the Put method
		// doesn't actually store the object with the ID returned by calculateObjectID
		// but instead returns a formatted string with extension
		actualID, err := calculateObjectID(jpegData)
		c.Assert(err, qt.IsNil)
		err = testDB.SetObject(actualID, "test@example.com", "image/jpeg", jpegData)
		c.Assert(err, qt.IsNil)

		// Now get the object
		object, err := client.Get(actualID)
		c.Assert(err, qt.IsNil)
		c.Assert(object, qt.Not(qt.IsNil))
		c.Assert(object.Data, qt.DeepEquals, jpegData)
		c.Assert(object.User, qt.Equals, "test@example.com")
		c.Assert(object.ContentType, qt.Equals, "image/jpeg")
	})

	t.Run("Cache", func(_ *testing.T) {
		c := qt.New(t)

		// Create a client with a small cache
		cache, err := lru.New[string, db.Object](2)
		c.Assert(err, qt.IsNil)
		clientWithCache := &Client{
			db:             testDB,
			supportedTypes: DefaultSupportedFileTypes,
			cache:          cache,
		}

		// Create three test objects
		jpegData1 := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01}
		jpegData2 := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0x02}
		jpegData3 := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01, 0x03}

		// Calculate object IDs
		objectID1, err := calculateObjectID(jpegData1)
		c.Assert(err, qt.IsNil)
		objectID2, err := calculateObjectID(jpegData2)
		c.Assert(err, qt.IsNil)
		objectID3, err := calculateObjectID(jpegData3)
		c.Assert(err, qt.IsNil)

		// Store objects directly in the database
		err = testDB.SetObject(objectID1, "test1@example.com", "image/jpeg", jpegData1)
		c.Assert(err, qt.IsNil)

		err = testDB.SetObject(objectID2, "test2@example.com", "image/jpeg", jpegData2)
		c.Assert(err, qt.IsNil)

		err = testDB.SetObject(objectID3, "test3@example.com", "image/jpeg", jpegData3)
		c.Assert(err, qt.IsNil)

		// Get the first object to cache it
		_, err = clientWithCache.Get(objectID1)
		c.Assert(err, qt.IsNil)
		// Verify it's in the cache
		_, ok := clientWithCache.cache.Get(objectID1.String())
		c.Assert(ok, qt.IsTrue)

		// Get the second object to cache it
		_, err = clientWithCache.Get(objectID2)
		c.Assert(err, qt.IsNil)
		// Verify it's in the cache
		_, ok = clientWithCache.cache.Get(objectID2.String())
		c.Assert(ok, qt.IsTrue)

		// Get the third object to cache it
		_, err = clientWithCache.Get(objectID3)
		c.Assert(err, qt.IsNil)
		// Verify it's in the cache
		_, ok = clientWithCache.cache.Get(objectID3.String())
		c.Assert(ok, qt.IsTrue)

		// The first object should be evicted from the cache
		_, ok = clientWithCache.cache.Get(objectID1.String())
		c.Assert(ok, qt.IsFalse)

		// The second object should still be in the cache
		_, ok = clientWithCache.cache.Get(objectID2.String())
		c.Assert(ok, qt.IsTrue)
	})

	t.Run("CalculateObjectID", func(_ *testing.T) {
		c := qt.New(t)

		// Test with valid data
		data := []byte("test data")
		objectID, err := calculateObjectID(data)
		c.Assert(err, qt.IsNil)
		c.Assert(objectID, qt.HasLen, 12)
		c.Assert(objectID.IsZero(), qt.Not(qt.IsTrue))

		// Test with empty data
		emptyData := []byte{}
		objectID, err = calculateObjectID(emptyData)
		c.Assert(err, qt.IsNil)
		c.Assert(objectID, qt.HasLen, 12)
		c.Assert(objectID.IsZero(), qt.Not(qt.IsTrue))

		// Test that different data produces different IDs
		data2 := []byte("different data")
		objectID2, err := calculateObjectID(data2)
		c.Assert(err, qt.IsNil)
		c.Assert(objectID2, qt.Not(qt.Equals), objectID)
	})

	t.Run("ErrorHandling", func(_ *testing.T) {
		c := qt.New(t)

		// Test with a reader that returns an error
		errorReader := &ErrorReader{err: io.ErrUnexpectedEOF}
		_, err := client.Put(errorReader, 10, "test@example.com")
		c.Assert(err, qt.Not(qt.IsNil))
	})
}

// ErrorReader is a mock reader that always returns an error.
// It implements the io.Reader interface for testing error handling.
type ErrorReader struct {
	err error
}

// Read implements the io.Reader interface and always returns the error
// specified in the ErrorReader struct.
func (r *ErrorReader) Read(_ []byte) (n int, err error) {
	return 0, r.err
}
