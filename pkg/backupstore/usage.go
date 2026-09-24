package backupstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ErrBucketMissing reports that a managed backup bucket does not exist at the
// measured endpoint. A sweep records zero for it and continues; it is a
// separate error so "no bucket" is never silently indistinguishable from
// "bucket holds nothing".
var ErrBucketMissing = errors.New("managed backup bucket does not exist")

// BucketUsage is one measurement of a managed backup bucket.
type BucketUsage struct {
	Bucket      string
	StoredBytes int64
	ObjectCount int64
}

// s3ListAPI is the read-only slice of the S3 client the meter uses. The meter
// deliberately cannot delete: it is the billing path, and a sweep that runs
// over every tenant's bucket while holding a delete verb is one bug away from
// being a data-loss path.
type s3ListAPI interface {
	s3.ListObjectsV2APIClient
}

// BucketMeter measures stored bytes through the R2 S3 API using the platform's
// account-level admin credentials.
type BucketMeter struct {
	client s3ListAPI
}

// BucketMeterFromEnv builds the meter from the platform R2 admin credentials.
// The jurisdiction must match the one the buckets were created in: a meter
// pointed at the wrong jurisdiction lists nothing and would report every
// tenant as using zero bytes, admitting every over-quota account.
func BucketMeterFromEnv(accountID, jurisdiction string) (*BucketMeter, error) {
	accessKey := strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY"))
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("backupstore usage requires R2_ACCESS_KEY_ID and R2_SECRET_ACCESS_KEY")
	}
	expected, err := S3EndpointFor(accountID, jurisdiction)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))
	if endpoint == "" {
		return NewBucketMeter(expected, accessKey, secretKey)
	}
	if !sameS3Host(endpoint, expected) {
		return nil, fmt.Errorf(
			"backupstore usage: R2_ENDPOINT addresses a different R2 jurisdiction than %q; unset it or align it with the configured jurisdiction",
			jurisdiction)
	}
	return NewBucketMeter(endpoint, accessKey, secretKey)
}

func NewBucketMeter(endpoint, accessKey, secretKey string) (*BucketMeter, error) {
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
	return &BucketMeter{client: client}, nil
}

// Measure sums every object in the bucket. This is deliberately the physical
// stored size at the supplier, not a logical figure derived from snapshot
// metadata: it is what the invoice is based on, and it is measured by kombify
// rather than reported by the party being measured.
func (m *BucketMeter) Measure(ctx context.Context, bucket string) (BucketUsage, error) {
	usage := BucketUsage{Bucket: bucket}
	paginator := s3.NewListObjectsV2Paginator(m.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			if isNoSuchBucket(err) {
				return BucketUsage{Bucket: bucket}, ErrBucketMissing
			}
			return BucketUsage{}, fmt.Errorf("list objects in %s: %w", bucket, err)
		}
		for _, object := range page.Contents {
			usage.StoredBytes += aws.ToInt64(object.Size)
			usage.ObjectCount++
		}
	}
	return usage, nil
}

// StoredUsage is a persisted measurement.
type StoredUsage struct {
	TenantID    string
	StackID     string
	Bucket      string
	StoredBytes int64
	ObjectCount int64
	MeasuredAt  time.Time
}

// ErrUsageNotFound reports that a stack has never been measured. Callers must
// not read it as zero usage: an unmeasured stack is unknown, not empty.
var ErrUsageNotFound = errors.New("managed backup usage not measured")

// PostgresUsageStore persists measurements under tenant RLS.
type PostgresUsageStore struct {
	db *sql.DB
}

func NewPostgresUsageStore(db *sql.DB) (*PostgresUsageStore, error) {
	if db == nil {
		return nil, fmt.Errorf("managed backup usage store requires a database handle")
	}
	return &PostgresUsageStore{db: db}, nil
}

// Record persists one measurement, replacing the previous one for the stack.
func (s *PostgresUsageStore) Record(ctx context.Context, tenantID, stackID string, usage BucketUsage) (StoredUsage, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	bucket := strings.TrimSpace(usage.Bucket)
	if tenantID == "" || stackID == "" || bucket == "" {
		return StoredUsage{}, fmt.Errorf("managed backup usage requires exact tenant, stack and bucket identity")
	}
	if usage.StoredBytes < 0 || usage.ObjectCount < 0 {
		return StoredUsage{}, fmt.Errorf("managed backup usage cannot be negative")
	}
	stored := StoredUsage{TenantID: tenantID, StackID: stackID}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			INSERT INTO stack_backup_usage (
				tenant_id, stack_id, bucket, stored_bytes, object_count, measured_at
			) VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
				bucket = EXCLUDED.bucket,
				stored_bytes = EXCLUDED.stored_bytes,
				object_count = EXCLUDED.object_count,
				measured_at = now(),
				updated_at = now()
			RETURNING bucket, stored_bytes, object_count, measured_at
		`, tenantID, stackID, bucket, usage.StoredBytes, usage.ObjectCount).Scan(
			&stored.Bucket, &stored.StoredBytes, &stored.ObjectCount, &stored.MeasuredAt)
	})
	if err != nil {
		return StoredUsage{}, fmt.Errorf("persist managed backup usage: %w", err)
	}
	return stored, nil
}

// Usage loads the last measurement for a stack.
func (s *PostgresUsageStore) Usage(ctx context.Context, tenantID, stackID string) (StoredUsage, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return StoredUsage{}, fmt.Errorf("managed backup usage requires exact tenant and stack identity")
	}
	stored := StoredUsage{TenantID: tenantID, StackID: stackID}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT bucket, stored_bytes, object_count, measured_at
			FROM stack_backup_usage
			WHERE tenant_id = $1 AND stack_id = $2
		`, tenantID, stackID).Scan(
			&stored.Bucket, &stored.StoredBytes, &stored.ObjectCount, &stored.MeasuredAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return StoredUsage{}, ErrUsageNotFound
	}
	if err != nil {
		return StoredUsage{}, fmt.Errorf("load managed backup usage: %w", err)
	}
	return stored, nil
}

func (s *PostgresUsageStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", custodyTenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// UsageTarget names one stack's bucket for a sweep.
type UsageTarget struct {
	TenantID string
	StackID  string
	Bucket   string
}

// SweepOutcome reports one target's result.
type SweepOutcome struct {
	Target  UsageTarget
	Usage   StoredUsage
	Missing bool
	Err     error
}

// Sweep measures and records every target. One failing target does not abort
// the others: a sweep that stops at the first unreachable bucket would leave
// every later tenant on a stale figure, which is the state the quota gate must
// never silently admit on.
func Sweep(ctx context.Context, meter *BucketMeter, store *PostgresUsageStore, targets []UsageTarget) []SweepOutcome {
	if meter == nil || store == nil {
		return nil
	}
	outcomes := make([]SweepOutcome, 0, len(targets))
	for _, target := range targets {
		outcome := SweepOutcome{Target: target}
		usage, err := meter.Measure(ctx, target.Bucket)
		switch {
		case errors.Is(err, ErrBucketMissing):
			// A provisioned stack whose bucket is gone genuinely stores
			// nothing, so record zero rather than leaving a stale figure.
			outcome.Missing = true
		case err != nil:
			outcome.Err = err
			outcomes = append(outcomes, outcome)
			continue
		}
		usage.Bucket = target.Bucket
		recorded, err := store.Record(ctx, target.TenantID, target.StackID, usage)
		if err != nil {
			outcome.Err = err
			outcomes = append(outcomes, outcome)
			continue
		}
		outcome.Usage = recorded
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
