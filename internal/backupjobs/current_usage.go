package backupjobs

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/pkg/backupstore"
)

// CurrentUsage measures the exact repository in encrypted tenant custody before
// admission. A missing bucket, inaccessible custody or failed measurement is
// unknown usage, never an empty repository. Credentials never leave this call.
type CurrentUsage struct {
	Custody interface {
		Get(context.Context, string, string) (backupstore.Credentials, error)
	}
	Store interface {
		Record(context.Context, string, string, backupstore.BucketUsage) (backupstore.StoredUsage, error)
	}
}

func (reader CurrentUsage) Usage(ctx context.Context, tenantID, stackID string) (backupstore.StoredUsage, error) {
	if reader.Custody == nil || reader.Store == nil || tenantID == "" || stackID == "" {
		return backupstore.StoredUsage{}, fmt.Errorf("managed backup usage authority is unavailable")
	}
	credentials, err := reader.Custody.Get(ctx, tenantID, stackID)
	if err != nil || credentials.TenantID != tenantID || credentials.StackID != stackID || credentials.Bucket == "" {
		return backupstore.StoredUsage{}, fmt.Errorf("managed backup usage custody is unavailable")
	}
	meter, err := backupstore.NewBucketMeter(credentials.Endpoint, credentials.AccessKeyID, credentials.SecretAccessKey)
	if err != nil {
		return backupstore.StoredUsage{}, fmt.Errorf("managed backup usage meter is unavailable")
	}
	usage, err := meter.Measure(ctx, credentials.Bucket)
	if err != nil {
		return backupstore.StoredUsage{}, fmt.Errorf("managed backup usage measurement failed")
	}
	return reader.Store.Record(ctx, tenantID, stackID, usage)
}
