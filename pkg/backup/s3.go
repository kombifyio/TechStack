// Package backup provides backup functionality including S3 storage and scheduling.
package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kombifyio/techstack/pkg/logger"
)

// S3Config holds S3 client configuration.
type S3Config struct {
	Bucket    string
	Endpoint  string // Custom endpoint for MinIO compatibility
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string // Key prefix for all objects
}

// S3Object represents metadata about an object in S3.
type S3Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
	ETag         string    `json:"etag,omitempty"`
}

// S3Client provides operations for S3-compatible storage.
type S3Client struct {
	client *s3.Client
	config S3Config
	log    *logger.Logger
}

// NewS3Client creates a new S3 client with the given configuration.
func NewS3Client(cfg S3Config) (*S3Client, error) {
	log := logger.Get().WithComponent("s3")

	if cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket name is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 access key and secret key are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	// Create custom credentials provider
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")

	// Build AWS config options
	opts := []func(*config.LoadOptions) error{
		config.WithCredentialsProvider(creds),
		config.WithRegion(cfg.Region),
	}

	// Load AWS config
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client with optional custom endpoint (for MinIO)
	s3Opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // Required for MinIO
		})
		log.Info("Using custom S3 endpoint", "endpoint", cfg.Endpoint)
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	log.Info("S3 client initialized",
		"bucket", cfg.Bucket,
		"region", cfg.Region,
		"prefix", cfg.Prefix,
	)

	return &S3Client{
		client: client,
		config: cfg,
		log:    log,
	}, nil
}

// Upload uploads a local file to S3.
func (c *S3Client) Upload(ctx context.Context, localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info for size
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	key := c.fullKey(remotePath)
	c.log.Info("Uploading to S3",
		"local_path", localPath,
		"s3_key", key,
		"size", info.Size(),
	)

	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.config.Bucket),
		Key:           aws.String(key),
		Body:          file,
		ContentLength: aws.Int64(info.Size()),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}

	c.log.Info("Upload complete", "s3_key", key)
	return nil
}

// Download downloads a file from S3 to a local path.
func (c *S3Client) Download(ctx context.Context, remotePath, localPath string) error {
	key := c.fullKey(remotePath)
	c.log.Info("Downloading from S3",
		"s3_key", key,
		"local_path", localPath,
	)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Get object from S3
	result, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to download from S3: %w", err)
	}
	defer result.Body.Close()

	// Create local file
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %w", err)
	}
	defer file.Close()

	// Copy content
	written, err := io.Copy(file, result.Body)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	c.log.Info("Download complete",
		"s3_key", key,
		"local_path", localPath,
		"size", written,
	)
	return nil
}

// Delete removes an object from S3.
func (c *S3Client) Delete(ctx context.Context, remotePath string) error {
	key := c.fullKey(remotePath)
	c.log.Info("Deleting from S3", "s3_key", key)

	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	c.log.Info("Delete complete", "s3_key", key)
	return nil
}

// List returns all objects in S3 with the given prefix.
func (c *S3Client) List(ctx context.Context, prefix string) ([]S3Object, error) {
	fullPrefix := c.fullKey(prefix)
	c.log.Debug("Listing S3 objects", "prefix", fullPrefix)

	var objects []S3Object
	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.config.Bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list S3 objects: %w", err)
		}

		for _, obj := range page.Contents {
			objects = append(objects, S3Object{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
			})
		}
	}

	c.log.Debug("Listed S3 objects", "count", len(objects))
	return objects, nil
}

// Exists checks if an object exists in S3.
func (c *S3Client) Exists(ctx context.Context, remotePath string) (bool, error) {
	key := c.fullKey(remotePath)

	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// Check if it's a "not found" error
		return false, nil
	}
	return true, nil
}

// DeleteMultiple removes multiple objects from S3.
// Returns an aggregated error if any deletions fail.
func (c *S3Client) DeleteMultiple(ctx context.Context, remotePaths []string) error {
	if len(remotePaths) == 0 {
		return nil
	}

	c.log.Info("Deleting multiple objects from S3", "count", len(remotePaths))

	// Delete objects one by one (for simplicity and compatibility)
	var errors []string
	var successCount int
	for _, path := range remotePaths {
		if err := c.Delete(ctx, path); err != nil {
			c.log.Warn("Failed to delete object", "path", path, "error", err)
			errors = append(errors, fmt.Sprintf("%s: %v", path, err))
		} else {
			successCount++
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to delete %d of %d objects: %s",
			len(errors), len(remotePaths), strings.Join(errors, "; "))
	}

	c.log.Info("Deleted objects from S3", "count", successCount)
	return nil
}

// fullKey returns the full S3 key with the configured prefix.
func (c *S3Client) fullKey(path string) string {
	if c.config.Prefix == "" {
		return path
	}
	return c.config.Prefix + path
}

// Bucket returns the configured bucket name.
func (c *S3Client) Bucket() string {
	return c.config.Bucket
}

// Prefix returns the configured key prefix.
func (c *S3Client) Prefix() string {
	return c.config.Prefix
}
