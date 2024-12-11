package objectstorage

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type ObjectStorageClient struct {
	config        aws.Config
	client        *s3.Client
	defaultBucket string
}

func New(apiKey, apiSecret, apiEndpoint, apiRegion, apiBucket string) (*ObjectStorageClient, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(apiKey, apiSecret, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("error seting up s3 session %v", err)
	}

	// s3Config := &aws.Config{
	// 	Credentials:      aws.NewStaticCredentials(apiKey, apiSecret, ""), // Specifies your credentials.
	// 	Endpoint:         aws.String(apiEndpoint),                               // Find your endpoint in the control panel, under Settings. Prepend "https://".
	// 	S3ForcePathStyle: aws.Bool(false),                                       // // Configures to use subdomain/virtual calling format. Depending on your version, alternatively use o.UsePathStyle = false
	// 	Region:           aws.String(apiRegion),                                 // Must be "us-east-1" when creating new Spaces. Otherwise, use the region in your endpoint, such as "nyc3".
	// }
	client := s3.NewFromConfig(cfg)
	return &ObjectStorageClient{
		config: cfg,
		client: client,
	}, nil
}

// key is set in a string and can have a directory like notation (for example "folder-path/hello-world.txt")
func (osc *ObjectStorageClient) Upload(key string, payload []byte) (string, error) {
	// The session the S3 Uploader will use
	// Create an uploader with the session and default options
	uploader := manager.NewUploader(osc.client)
	// Upload the file to S3.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	out, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(osc.defaultBucket), // The path to the directory you want to upload the object to, starting with your Space name.	Bucket: aws.String(myBucket),
		Key:    aws.String(key),               // Object key, referenced whenever you want to access this file later.	Key:    aws.String(myString),
		Body:   bytes.NewReader(payload),      // The object's contents.
		// ACL:    s3.ObjectCannedACLPrivate,     // Defines Access-control List (ACL) permissions, such as private or public.
		// Metadata: map[string]*string{ // Required. Defines metadata tags.
		// 	"x-amz-meta-my-key": aws.String("your-value"),
		// },
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload file, %v", err)
	}
	// object := s3.PutObjectInput{
	// 	Bucket: aws.String(osc.config.apiBucket),           // The path to the directory you want to upload the object to, starting with your Space name.
	// 	Key:    aws.String("folder-path/hello-world.txt"), // Object key, referenced whenever you want to access this file later.
	// }

	// _, err := osc.client.PutObject(&object)
	// if err != nil {
	// 	return err
	// }
	return out.Location, nil
}

// key is set in a string and can have a directory like notation (for example "folder-path/hello-world.txt")
func (osc *ObjectStorageClient) Get(key string) ([]byte, error) {
	// The session the S3 Downloader will use
	// sess := session.Must(session.NewSession(os.config))

	// Create a downloader with the session and default options
	downloader := manager.NewDownloader(osc.client)

	// Create a file to write the S3 Object contents to.
	// downloadFile, err := os.Create("downloaded-file")
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to create file %v", err)
	// }
	// defer downloadFile.Close()

	downloadFile := manager.NewWriteAtBuffer([]byte{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	// Write the contents of S3 Object to the file, returns the number of bytes
	numBytes, err := downloader.Download(ctx, downloadFile, &s3.GetObjectInput{
		Bucket: aws.String(osc.defaultBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download file, %v", err)
	}
	fmt.Printf("file downloaded, %d bytes\n", numBytes)
	return downloadFile.Bytes(), nil
}
