package backupstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// sizedFakeS3 serves pages of sized objects. It implements only the listing
// half of the S3 API, which is the point: the meter must not be able to delete.
type sizedFakeS3 struct {
	pages     [][]types.Object
	listCalls int
	listErr   error
}

func (f *sizedFakeS3) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	page := f.listCalls
	f.listCalls++
	out := &s3.ListObjectsV2Output{Contents: f.pages[page]}
	if page < len(f.pages)-1 {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(fmt.Sprintf("page-%d", page+1))
	} else {
		out.IsTruncated = aws.Bool(false)
	}
	return out, nil
}

func object(key string, size int64) types.Object {
	return types.Object{Key: aws.String(key), Size: aws.Int64(size)}
}

func TestMeasureSumsEveryPage(t *testing.T) {
	fake := &sizedFakeS3{pages: [][]types.Object{
		{object("kopia.blobcfg", 128), object("p00/abc", 20<<20)},
		{object("p01/def", 30<<20)},
	}}
	meter := &BucketMeter{client: fake}

	usage, err := meter.Measure(context.Background(), "skbk-stack-a")
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	if usage.ObjectCount != 3 {
		t.Fatalf("object count = %d, want 3", usage.ObjectCount)
	}
	want := int64(128) + int64(20<<20) + int64(30<<20)
	if usage.StoredBytes != want {
		t.Fatalf("stored bytes = %d, want %d", usage.StoredBytes, want)
	}
	if fake.listCalls != 2 {
		t.Fatalf("paginator stopped after %d pages; a partial sum would under-bill", fake.listCalls)
	}
}

// TestMeasureDistinguishesAMissingBucketFromAnEmptyOne matters because both
// report zero bytes, and only one of them is a correct answer. A sweep may
// record zero for a bucket that is genuinely gone; it must not silently do so
// because the endpoint was wrong.
func TestMeasureDistinguishesAMissingBucketFromAnEmptyOne(t *testing.T) {
	missing := &BucketMeter{client: &sizedFakeS3{listErr: errors.New("api error NoSuchBucket: not found")}}
	if _, err := missing.Measure(context.Background(), "skbk-gone"); !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("missing bucket must report ErrBucketMissing, got %v", err)
	}

	empty := &BucketMeter{client: &sizedFakeS3{pages: [][]types.Object{{}}}}
	usage, err := empty.Measure(context.Background(), "skbk-empty")
	if err != nil {
		t.Fatalf("empty bucket must not error: %v", err)
	}
	if usage.StoredBytes != 0 || usage.ObjectCount != 0 {
		t.Fatalf("empty bucket usage = %+v", usage)
	}

	other := &BucketMeter{client: &sizedFakeS3{listErr: errors.New("api error AccessDenied")}}
	if _, err := other.Measure(context.Background(), "skbk-denied"); err == nil || errors.Is(err, ErrBucketMissing) {
		t.Fatalf("an access failure must surface as an error, not as zero usage: %v", err)
	}
}

func TestUsageNotMeasuredIsNotZeroUsage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPostgresUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT bucket, stored_bytes, object_count, measured_at").
		WithArgs("tenant-a", "stack-a").
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "stored_bytes", "object_count", "measured_at"}))

	if _, err := store.Usage(context.Background(), "tenant-a", "stack-a"); !errors.Is(err, ErrUsageNotFound) {
		t.Fatalf("an unmeasured stack must not read as zero usage, got %v", err)
	}
}

func TestRecordPersistsUnderTenantContext(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPostgresUsageStore(db)
	if err != nil {
		t.Fatal(err)
	}
	measuredAt := time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO stack_backup_usage").
		WithArgs("tenant-a", "stack-a", "skbk-stack-a", int64(4096), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "stored_bytes", "object_count", "measured_at"}).
			AddRow("skbk-stack-a", int64(4096), int64(2), measuredAt))
	mock.ExpectCommit()

	stored, err := store.Record(context.Background(), "tenant-a", "stack-a",
		BucketUsage{Bucket: "skbk-stack-a", StoredBytes: 4096, ObjectCount: 2})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if stored.StoredBytes != 4096 || !stored.MeasuredAt.Equal(measuredAt) {
		t.Fatalf("stored = %+v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("tenant context or insert not as expected: %v", err)
	}
}

// TestSweepContinuesPastAFailingTarget pins the property that makes a nightly
// sweep usable: stopping at the first unreachable bucket would leave every
// later tenant on a stale figure, and the quota gate would keep admitting on it.
func TestSweepContinuesPastAFailingTarget(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresUsageStore(db)
	meter := &BucketMeter{client: &sizedFakeS3{listErr: errors.New("api error AccessDenied")}}

	outcomes := Sweep(context.Background(), meter, store, []UsageTarget{
		{TenantID: "tenant-a", StackID: "stack-a", Bucket: "skbk-stack-a"},
		{TenantID: "tenant-b", StackID: "stack-b", Bucket: "skbk-stack-b"},
	})

	if len(outcomes) != 2 {
		t.Fatalf("sweep returned %d outcomes, want 2", len(outcomes))
	}
	for _, outcome := range outcomes {
		if outcome.Err == nil {
			t.Fatalf("expected a recorded failure for %s", outcome.Target.StackID)
		}
	}
	// No write may have been attempted for a target that could not be measured.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmeasured targets must not be persisted: %v", err)
	}
}
