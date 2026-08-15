package backupstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3 struct {
	pages       [][]string
	listCalls   int
	deleteCalls [][]string
	deleteOut   []*s3.DeleteObjectsOutput
	listErr     error
}

func (f *fakeS3) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	page := f.listCalls
	f.listCalls++
	out := &s3.ListObjectsV2Output{}
	for _, key := range f.pages[page] {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(key)})
	}
	if page < len(f.pages)-1 {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(fmt.Sprintf("page-%d", page+1))
	} else {
		out.IsTruncated = aws.Bool(false)
	}
	return out, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, params *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	var keys []string
	for _, object := range params.Delete.Objects {
		keys = append(keys, aws.ToString(object.Key))
	}
	f.deleteCalls = append(f.deleteCalls, keys)
	if len(f.deleteOut) > 0 {
		out := f.deleteOut[0]
		f.deleteOut = f.deleteOut[1:]
		return out, nil
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func TestPurgeBucketPaginatesAndDeletesAll(t *testing.T) {
	fake := &fakeS3{pages: [][]string{{"a", "b"}, {"c"}}}
	purger := &S3Purger{client: fake}
	if err := purger.PurgeBucket(context.Background(), "skbk-x"); err != nil {
		t.Fatalf("PurgeBucket: %v", err)
	}
	if fake.listCalls != 2 || len(fake.deleteCalls) != 2 {
		t.Fatalf("calls: list=%d delete=%v", fake.listCalls, fake.deleteCalls)
	}
	if strings.Join(fake.deleteCalls[0], ",") != "a,b" || strings.Join(fake.deleteCalls[1], ",") != "c" {
		t.Fatalf("delete batches: %v", fake.deleteCalls)
	}
}

func TestPurgeBucketSurfacesQuietModePerKeyErrors(t *testing.T) {
	fake := &fakeS3{
		pages: [][]string{{"a", "b"}},
		deleteOut: []*s3.DeleteObjectsOutput{{
			Errors: []types.Error{{
				Key:     aws.String("b"),
				Message: aws.String("AccessDenied"),
			}},
		}},
	}
	purger := &S3Purger{client: fake}
	err := purger.PurgeBucket(context.Background(), "skbk-x")
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("quiet-mode errors must abort the purge, got %v", err)
	}
}

func TestPurgeBucketTreatsMissingBucketAsPurged(t *testing.T) {
	fake := &fakeS3{listErr: errors.New("api error NoSuchBucket: The specified bucket does not exist")}
	purger := &S3Purger{client: fake}
	if err := purger.PurgeBucket(context.Background(), "skbk-x"); err != nil {
		t.Fatalf("missing bucket must be idempotent: %v", err)
	}
}
