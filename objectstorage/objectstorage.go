package objectstorage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/vocdoni/saas-backend/db"
)

var (
	ErrorObjectNotFound       = fmt.Errorf("object not found")
	ErrorInvalidObjectID      = fmt.Errorf("invalid object ID")
	ErrorFileTypeNotSupported = fmt.Errorf("file type not supported")
)

type ObjectFileType string

const (
	FileTypeJPEG ObjectFileType = "image/jpeg"
	FileTypePNG  ObjectFileType = "image/png"
	FileTypeJPG  ObjectFileType = "image/jpg"
)

var DefaultSupportedFileTypes = map[ObjectFileType]bool{
	FileTypeJPEG: true,
	FileTypePNG:  true,
	FileTypeJPG:  true,
}

type ObjectStorageConfig struct {
	DB             *db.MongoStorage
	SupportedTypes []ObjectFileType
	ServerURL      string
}

type ObjectStorageClient struct {
	db             *db.MongoStorage
	supportedTypes map[ObjectFileType]bool
	cache          *lru.Cache[string, db.Object]
	ServerURL      string
}

// New initializes a new ObjectStorageClient with the provided API credentials and configuration.
// It sets up a MinIO client and verifies the existence of the specified bucket.
func New(conf *ObjectStorageConfig) (*ObjectStorageClient, error) {
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
	return &ObjectStorageClient{
		db:             conf.DB,
		supportedTypes: supportedTypes,
		cache:          cache,
		ServerURL:      conf.ServerURL,
	}, nil
}

// key is set in a string and can have a directory like notation (for example "folder-path/hello-world.txt")
func (osc *ObjectStorageClient) Get(objectID string) (*db.Object, error) {
	if objectID == "" {
		return nil, ErrorInvalidObjectID
	}

	// check if the object is in the cache
	if object, ok := osc.cache.Get(objectID); ok {
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
	osc.cache.Add(objectID, *object)

	return object, nil
}

// uploadObject uploads the object image with the given objectID, associated to
// the user with the given userFID and the community with the given communityID.
// If the objectID is empty, it calculates the objectID from the data. It returns
// the URL of the uploaded object image. It stores the object in the database.
// If an error occurs, it returns an empty string and the error.
func (osc *ObjectStorageClient) Put(data io.Reader, size int64, userID string) (string, error) {
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
	if err := osc.db.SetObject(objectID, userID, filetype, buff); err != nil {
		return "", fmt.Errorf("cannot set object: %w", err)
	}
	// return objectURL(osc.serverURL, objectID, fileExtension), nil
	return fmt.Sprintf("%s.%s", objectID, fileExtension), nil
}

// calculateObjectID calculates the objectID from the given data. The objectID
// is the first 12 bytes of the md5 hash of the data. If an error occurs, it
// returns an empty string and the error.
func calculateObjectID(data []byte) (string, error) {
	md5hash := md5.New()
	if _, err := md5hash.Write(data); err != nil {
		return "", fmt.Errorf("cannot calculate hash: %w", err)
	}
	bhash := md5hash.Sum(nil)[:12]
	return hex.EncodeToString(bhash), nil
}
