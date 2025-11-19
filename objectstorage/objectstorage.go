package objectstorage

import (
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	// ErrorObjectNotFound is returned when the requested object is not found in storage.
	ErrorObjectNotFound = fmt.Errorf("object not found")
	// ErrorInvalidObjectID is returned when the provided object ID is invalid or empty.
	ErrorInvalidObjectID = fmt.Errorf("invalid object ID")
	// ErrorFileTypeNotSupported is returned when the file type is not in the supported types list.
	ErrorFileTypeNotSupported = fmt.Errorf("file type not supported")
)

// ObjectFileType represents the MIME type of a stored object file.
type ObjectFileType string

const (
	// FileTypeJPEG represents the JPEG image MIME type.
	FileTypeJPEG ObjectFileType = "image/jpeg"
	// FileTypePNG represents the PNG image MIME type.
	FileTypePNG ObjectFileType = "image/png"
	// FileTypeJPG represents the JPG image MIME type.
	FileTypeJPG ObjectFileType = "image/jpg"
)

// DefaultSupportedFileTypes is a map of file types that are supported by default.
var DefaultSupportedFileTypes = map[ObjectFileType]bool{
	FileTypeJPEG: true,
	FileTypePNG:  true,
	FileTypeJPG:  true,
}

// Config holds the configuration for the object storage client.
// It includes the MongoDB storage, supported file types, and server URL.
type Config struct {
	DB             *db.MongoStorage
	SupportedTypes []ObjectFileType
	ServerURL      string
}

// Client provides functionality for storing and retrieving objects.
// It uses MongoDB for storage and includes an LRU cache for improved performance.
type Client struct {
	db             *db.MongoStorage
	supportedTypes map[ObjectFileType]bool
	cache          *lru.Cache[string, db.Object]
	ServerURL      string
}

// New initializes a new ObjectStorageClient with the provided API credentials and configuration.
// It sets up a MinIO client and verifies the existence of the specified bucket.
func New(conf *Config) (*Client, error) {
	if conf == nil {
		return nil, fmt.Errorf("invalid object storage configuration")
	}
	supportedTypes := DefaultSupportedFileTypes
	for _, t := range conf.SupportedTypes {
		supportedTypes[t] = true
	}
	cache, err := lru.New[string, db.Object](256)
	if err != nil {
		return nil, fmt.Errorf("cannot create cache: %w", err)
	}
	return &Client{
		db:             conf.DB,
		supportedTypes: supportedTypes,
		cache:          cache,
		ServerURL:      conf.ServerURL,
	}, nil
}

// Get retrieves an object from storage by its ID. It first checks the cache,
// and if not found, retrieves it from the database. The objectID is a string
// that can have a directory-like notation (e.g., "folder-path/hello-world.txt").
// Returns the object or an error if not found or invalid.
func (osc *Client) Get(objectID internal.ObjectID) (*db.Object, error) {
	if objectID.IsZero() {
		return nil, ErrorInvalidObjectID
	}

	// check if the object is in the cache
	if object, ok := osc.cache.Get(objectID.String()); ok {
		return &object, nil
	}

	object, err := osc.db.Object(objectID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, ErrorObjectNotFound
		}
		return nil, fmt.Errorf("error retrieving object: %w", err)
	}

	// store the object in the cache
	osc.cache.Add(objectID.String(), *object)

	return object, nil
}

// Put uploads a object image associated to a user (free-form string)
//
//	It calculates the objectID from the data and uses that as filename. It returns
//
// the URL of the uploaded object image. It stores the object in the database.
// If an error occurs, it returns an empty string and the error.
func (osc *Client) Put(data io.Reader, size int64, user string) (string, error) {
	// Create a buffer of the appropriate size
	buff := make([]byte, size)
	_, err := data.Read(buff)
	if err != nil {
		return "", fmt.Errorf("cannot read file %s", err.Error())
	}
	// checking the content type
	// so we don't allow files other than images
	filetype := http.DetectContentType(buff)
	// extract type/extesion from the filetype
	fileExtension := strings.Split(filetype, "/")[1]

	if !osc.supportedTypes[ObjectFileType(filetype)] {
		return "ObjectFileType", ErrorFileTypeNotSupported
	}

	objectID, err := calculateObjectID(buff)
	if err != nil {
		return "", fmt.Errorf("error calculating objectID: %w", err)
	}

	// store the object in the database
	if err := osc.db.SetObject(objectID, user, filetype, buff); err != nil {
		return "", fmt.Errorf("cannot set object: %w", err)
	}
	// return objectURL(osc.serverURL, objectID, fileExtension), nil
	return fmt.Sprintf("%s.%s", objectID, fileExtension), nil
}

// calculateObjectID calculates the objectID from the given data. The objectID
// is the first 12 bytes of the md5 hash of the data. If an error occurs, it
// returns a NilObjectID and the error.
func calculateObjectID(data []byte) (internal.ObjectID, error) {
	md5hash := md5.New()
	if _, err := md5hash.Write(data); err != nil {
		return internal.NilObjectID, fmt.Errorf("cannot calculate hash: %w", err)
	}
	bhash := md5hash.Sum(nil)[:12]
	return internal.ObjectID(bhash), nil
}
