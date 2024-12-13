package objectstorage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.vocdoni.io/dvote/log"
)

type ObjectStorageClient struct {
	config *minio.Options
	client *minio.Client
	bucket *minio.BucketInfo
}

// New initializes a new ObjectStorageClient with the provided API credentials and configuration.
// It sets up a MinIO client and verifies the existence of the specified bucket.
func New(apiKey, apiSecret, apiEndpoint, apiRegion, apiBucket string) (*ObjectStorageClient, error) {
	config := &minio.Options{
		Creds:  credentials.NewStaticV4(apiKey, apiSecret, ""),
		Secure: true,
		Region: apiRegion,
	}
	// Initialize minio client object.
	minioClient, err := minio.New(apiEndpoint, config)
	if err != nil {
		return nil, fmt.Errorf("error seting up s3 session %v", err)
	}
	if exists, err := minioClient.BucketExists(context.Background(), apiBucket); err != nil || !exists {
		return nil, fmt.Errorf("bucket %s not found: %v", apiBucket, err)
	}
	if policy, err := minioClient.GetBucketPolicy(context.Background(), apiBucket); err == nil {
		log.Debugf("bucket policy: %s", policy)
	}
	return &ObjectStorageClient{
		config: config,
		client: minioClient,
		bucket: &minio.BucketInfo{
			Name: apiBucket,
		},
	}, nil
}

// Put uploads a file to the object storage service.
func (osc *ObjectStorageClient) Put(
	key, contentDisposition, contentType string, size int64, payload io.Reader, metadata map[string]string,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()

	info, err := osc.client.PutObject(ctx, osc.bucket.Name, key, payload, size, minio.PutObjectOptions{
		ContentType:        contentType,
		ContentDisposition: contentDisposition,
		UserMetadata:       metadata,
	})
	if err != nil {
		return "", fmt.Errorf("failed to uploade file %s :  %v", key, err)
	}
	if info.Size != size {
		return "", fmt.Errorf("failed to uploade file %s :  upload size differ with received size", key)
	}
	// TODO return correct url since info.location is empty
	return info.Location, nil
}

// key is set in a string and can have a directory like notation (for example "folder-path/hello-world.txt")
func (osc *ObjectStorageClient) Get(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()

	object, err := osc.client.GetObject(ctx, osc.bucket.Name, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %s: %v", key, err)
	}
	defer func() {
		if err := object.Close(); err != nil {
			return
		}
	}()

	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, object); err != nil {
		return nil, fmt.Errorf("failed to read object %s: %v", key, err)
	}

	return buffer.Bytes(), nil
}
