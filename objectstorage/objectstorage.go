package objectstorage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/vocdoni/saas-backend/db"
)

type ObjectStorageConfig struct {
	DB        *db.MongoStorage
	ServerURL string
}

type ObjectStorageClient struct {
	db        *db.MongoStorage
	serverURL string
}

var isObjectNameRgx = regexp.MustCompile(`^([a-zA-Z0-9]+)\.(jpg|jpeg|png)`)

// New initializes a new ObjectStorageClient with the provided API credentials and configuration.
// It sets up a MinIO client and verifies the existence of the specified bucket.
func New(conf *ObjectStorageConfig) *ObjectStorageClient {
	if conf == nil {
		return nil
	}
	return &ObjectStorageClient{
		db:        conf.DB,
		serverURL: conf.ServerURL,
	}
}

// key is set in a string and can have a directory like notation (for example "folder-path/hello-world.txt")
func (osc *ObjectStorageClient) Get(objectName string) (*db.Object, error) {
	if objectName == "" {
		return nil, fmt.Errorf("objectID is empty")
	}
	objectID, ok := objectIDfromName(objectName)
	if !ok {
		return nil, fmt.Errorf("invalid objectID")
	}

	object, err := osc.db.Object(objectID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, fmt.Errorf("object not found")
		}
		return nil, fmt.Errorf("cannot get object: %w", err)
	}

	return object, nil
}

// uploadObject uploads the object image with the given objectID, associated to
// the user with the given userFID and the community with the given communityID.
// If the objectID is empty, it calculates the objectID from the data. It returns
// the URL of the uploaded object image. It stores the object in the database.
// If an error occurs, it returns an empty string and the error.
func (osc *ObjectStorageClient) Put(data io.Reader, size int64, userID string) (string, error) {
	// if !isBase64Image(data) {
	// 	return "", fmt.Errorf("image is not base64 encoded")
	// }

	// Get the expected size of the data

	// Create a buffer of the appropriate size
	buff := make([]byte, size)
	_, err := data.Read(buff)
	if err != nil {
		return "", fmt.Errorf("cannot read file %s", err.Error())
	}
	// checking the content type
	// so we don't allow files other than images
	filetype := http.DetectContentType(buff)
	if filetype != "image/jpeg" && filetype != "image/png" && filetype != "image/jpg" {
		return "", fmt.Errorf("The provided file format is not allowed. Please upload a JPEG,JPG or PNG image")
	}

	fileExtension := strings.Split(filetype, "/")[1]

	objectID, err := calculateObjectID(buff)
	if err != nil {
		return "", fmt.Errorf("error calculating objectID: %w", err)
	}
	// store the object in the database
	if err := osc.db.SetObject(objectID, userID, filetype, buff); err != nil {
		return "", fmt.Errorf("cannot set object: %w", err)
	}
	return objectURL(osc.serverURL, objectID, fileExtension), nil
}

// objectURL returns the URL for the object with the given objectID.
func objectURL(baseURL, objectID, extension string) string {
	return fmt.Sprintf("%s/storage/%s.%s", baseURL, objectID, extension)
}

// objectIDfromURL returns the objectID from the given URL. If the URL is not an
// object URL, it returns an empty string and false.
func objectIDfromName(url string) (string, bool) {
	objectID := isObjectNameRgx.FindStringSubmatch(url)
	if len(objectID) != 3 {
		return "", false
	}
	return objectID[1], true
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
