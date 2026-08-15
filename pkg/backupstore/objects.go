package backupstore

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3ObjectAPI is the slice of the S3 client the purger uses; faked in tests.
type s3ObjectAPI interface {
	s3.ListObjectsV2APIClient
	DeleteObjects(ctx context.Context, params *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// S3Purger empties managed backup buckets through the R2 S3 API using the
// platform's account-level admin S3 credentials. The encrypted per-stack
// custody is reserved for authenticated stack operations and is not coupled
// to this control-plane-last cleanup path.
type S3Purger struct {
	client s3ObjectAPI
}

// S3PurgerFromEnv builds the purger from the platform R2 admin credentials.
func S3PurgerFromEnv(accountID string) (*S3Purger, error) {
	accessKey := strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY"))
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("backupstore purge requires R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY")
	}
	endpoint := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	}
	return NewS3Purger(endpoint, accessKey, secretKey)
}

func NewS3Purger(endpoint, accessKey, secretKey string) (*S3Purger, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	return &S3Purger{client: client}, nil
}

// PurgeBucket deletes every object in the bucket in batches. A missing
// bucket is treated as already purged.
func (p *S3Purger) PurgeBucket(ctx context.Context, bucket string) error {
	paginator := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			if isNoSuchBucket(err) {
				return nil
			}
			return fmt.Errorf("list objects: %w", err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key})
		}
		out, err := p.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
		// Quiet mode reports failures ONLY through the Errors slice — a
		// partial batch failure must abort the wipe (bucket deletion would
		// fail on the leftovers, or worse, backups would silently survive).
		if out != nil && len(out.Errors) > 0 {
			first := out.Errors[0]
			return fmt.Errorf("failed to delete %d object(s) in %s, first: %s (%s)",
				len(out.Errors), bucket, aws.ToString(first.Key), aws.ToString(first.Message))
		}
	}
	return nil
}

func isNoSuchBucket(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NoSuchBucket")
}
