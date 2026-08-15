package vmleases

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestClassifyLeaseInventoryIsClosedAndFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	activeLease := testLease(now)

	tests := []struct {
		name       string
		row        leaseInventoryRow
		wantState  LeaseAuthorityState
		wantActive bool
		wantErr    error
	}{
		{
			name:      "unbound",
			row:       leaseInventoryRow{lease: activeLease},
			wantState: LeaseAuthorityStateUnbound,
		},
		{
			name: "legacy quarantine",
			row: leaseInventoryRow{
				lease: activeLease, authority: LeaseExecutionAuthorityLegacySimulate, authorityBound: true,
			},
			wantState: LeaseAuthorityStateLegacyQuarantined,
		},
		{
			name: "native active",
			row: leaseInventoryRow{
				lease: activeLease, authority: LeaseExecutionAuthorityTechStackProviderControl, authorityBound: true,
			},
			wantState:  LeaseAuthorityStateNativeActive,
			wantActive: true,
		},
		{
			name: "native stopped",
			row: leaseInventoryRow{
				lease: func() vmlease.Lease {
					lease := cloneLease(activeLease)
					lease.DesiredState = vmlease.DesiredStateStopped
					return lease
				}(),
				authority: LeaseExecutionAuthorityTechStackProviderControl, authorityBound: true,
			},
			wantState: LeaseAuthorityStateNativeInactive,
		},
		{
			name: "native expired",
			row: leaseInventoryRow{
				lease: func() vmlease.Lease {
					lease := cloneLease(activeLease)
					lease.ValidUntil = now
					return lease
				}(),
				authority: LeaseExecutionAuthorityTechStackProviderControl, authorityBound: true,
			},
			wantState: LeaseAuthorityStateNativeInactive,
		},
		{
			name: "native not yet valid",
			row: leaseInventoryRow{
				lease: func() vmlease.Lease {
					lease := cloneLease(activeLease)
					lease.ValidFrom = now.Add(time.Minute)
					lease.ValidUntil = now.Add(time.Hour)
					return lease
				}(),
				authority: LeaseExecutionAuthorityTechStackProviderControl, authorityBound: true,
			},
			wantState: LeaseAuthorityStateNativeInactive,
		},
		{
			name: "native invalid contract",
			row: leaseInventoryRow{
				lease: func() vmlease.Lease {
					lease := cloneLease(activeLease)
					lease.LifecycleClass = vmlease.LifecycleClassOneTime
					return lease
				}(),
				authority: LeaseExecutionAuthorityTechStackProviderControl, authorityBound: true,
			},
			wantState: LeaseAuthorityStateNativeInactive,
		},
		{
			name: "unknown authority",
			row: leaseInventoryRow{
				lease: activeLease, authority: LeaseExecutionAuthority("executor_ready"), authorityBound: true,
			},
			wantErr: ErrLeaseExecutionAuthorityUnbound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record, err := classifyLeaseInventory(tt.row, now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("classify error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if record.AuthorityState != tt.wantState {
				t.Fatalf("authority state = %q, want %q", record.AuthorityState, tt.wantState)
			}
			if record.NativeActive() != tt.wantActive {
				t.Fatalf("NativeActive = %v, want %v", record.NativeActive(), tt.wantActive)
			}
		})
	}
}

func TestMemoryStoreInventorySnapshotIsTenantScoped(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	nativeLease := testLease(now)
	nativeLease.ID = vmlease.LeaseID("lease-native")
	unboundLease := testLease(now)
	unboundLease.ID = vmlease.LeaseID("lease-unbound")
	otherTenantLease := testLease(now)
	otherTenantLease.ID = vmlease.LeaseID("lease-other")
	otherTenantLease.Subject.OrgID = "org-2"
	for _, lease := range []vmlease.Lease{nativeLease, unboundLease, otherTenantLease} {
		if _, err := store.Upsert(context.Background(), lease, ""); err != nil {
			t.Fatalf("Upsert(%s): %v", lease.ID, err)
		}
	}
	store.executionAuthorities[memoryExecutionAuthorityKey{tenantID: "org-1", leaseID: nativeLease.ID}] = LeaseExecutionAuthorityTechStackProviderControl
	store.executionAuthorities[memoryExecutionAuthorityKey{tenantID: "org-2", leaseID: otherTenantLease.ID}] = LeaseExecutionAuthorityTechStackProviderControl

	service := NewService(store, ServiceConfig{Now: func() time.Time { return now }})
	records, err := service.ListInventoryByTenant(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListInventoryByTenant: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Lease.ID != nativeLease.ID || !records[0].NativeActive() {
		t.Fatalf("first record = %#v, want active native lease", records[0])
	}
	if records[1].Lease.ID != unboundLease.ID || records[1].AuthorityState != LeaseAuthorityStateUnbound {
		t.Fatalf("second record = %#v, want unbound lease", records[1])
	}
	if _, err := service.GetInventory(context.Background(), "org-1", otherTenantLease.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant GetInventory error = %v, want not found", err)
	}
}

func TestPostgresStoreListInventoryUsesSingleTenantScopedJoin(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	nativeLease := testLease(now)
	nativeLease.ID = vmlease.LeaseID("lease-native")
	nativePayload, err := json.Marshal(nativeLease)
	if err != nil {
		t.Fatal(err)
	}
	unboundLease := testLease(now)
	unboundLease.ID = vmlease.LeaseID("lease-unbound")
	unboundPayload, err := json.Marshal(unboundLease)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("org-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT lease\.lease_json::text, lease\.desired_state,[\s\S]+authority\.execution_authority[\s\S]+LEFT JOIN runtime_lease_execution_authorities AS authority[\s\S]+authority\.tenant_id = lease\.tenant_id AND authority\.lease_id = lease\.id[\s\S]+WHERE lease\.tenant_id = \$1`).
		WithArgs("org-1").
		WillReturnRows(sqlmock.NewRows([]string{"lease_json", "desired_state", "valid_from", "valid_until", "renewed_at", "cancelled_at", "execution_authority"}).
			AddRow(nativePayload, "running", nativeLease.ValidFrom, nativeLease.ValidUntil, nativeLease.RenewedAt, nil, "techstack_provider_control").
			AddRow(unboundPayload, "running", unboundLease.ValidFrom, unboundLease.ValidUntil, unboundLease.RenewedAt, nil, nil))
	mock.ExpectCommit()

	records, err := NewService(NewPostgresStore(db), ServiceConfig{Now: func() time.Time { return now }}).
		ListInventoryByTenant(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("ListInventoryByTenant: %v", err)
	}
	if len(records) != 2 || !records[0].NativeActive() || records[1].AuthorityState != LeaseAuthorityStateUnbound {
		t.Fatalf("records = %#v", records)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreGetInventoryRejectsUnknownAuthority(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	lease := testLease(now)
	payload, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("org-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT lease\.lease_json::text, lease\.desired_state,[\s\S]+authority\.execution_authority[\s\S]+WHERE lease\.tenant_id = \$1 AND lease\.id = \$2`).
		WithArgs("org-1", lease.ID).
		WillReturnRows(sqlmock.NewRows([]string{"lease_json", "desired_state", "valid_from", "valid_until", "renewed_at", "cancelled_at", "execution_authority"}).
			AddRow(payload, "running", lease.ValidFrom, lease.ValidUntil, lease.RenewedAt, nil, "executor_ready"))
	mock.ExpectCommit()

	_, err = NewService(NewPostgresStore(db), ServiceConfig{Now: func() time.Time { return now }}).
		GetInventory(context.Background(), "org-1", lease.ID)
	if !errors.Is(err, ErrLeaseExecutionAuthorityUnbound) {
		t.Fatalf("GetInventory error = %v, want authority-unbound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresInventoryCanonicalAbsentCannotReactivateNativeAuthority(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	lease := testLease(now.Add(-24 * time.Hour))
	lease.DesiredState = vmlease.DesiredStateRunning
	lease.CancelledAt = nil
	payload, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	cancelledAt := now.Add(-time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`SELECT set_config('app.tenant_id', $1, true)`)).
		WithArgs("org-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT lease\.lease_json::text, lease\.desired_state,[\s\S]+authority\.execution_authority[\s\S]+WHERE lease\.tenant_id = \$1 AND lease\.id = \$2`).
		WithArgs("org-1", lease.ID).
		WillReturnRows(sqlmock.NewRows([]string{"lease_json", "desired_state", "valid_from", "valid_until", "renewed_at", "cancelled_at", "execution_authority"}).
			AddRow(payload, "absent", lease.ValidFrom, lease.ValidUntil, lease.RenewedAt, cancelledAt, "techstack_provider_control"))
	mock.ExpectCommit()

	record, err := NewService(NewPostgresStore(db), ServiceConfig{Now: func() time.Time { return now }}).
		GetInventory(t.Context(), "org-1", lease.ID)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if record.NativeActive() || record.AuthorityState != LeaseAuthorityStateNativeInactive ||
		record.Lease.DesiredState != vmlease.DesiredStateArchived || record.Lease.CancelledAt == nil {
		t.Fatalf("canonical absent inventory = %#v", record)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type inventoryBlindStore struct {
	store *MemoryStore
}

func (s inventoryBlindStore) Upsert(ctx context.Context, lease vmlease.Lease, key string) (*vmlease.Lease, error) {
	return s.store.Upsert(ctx, lease, key)
}

func (s inventoryBlindStore) Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error) {
	return s.store.Get(ctx, tenantID, id)
}

func (s inventoryBlindStore) Update(ctx context.Context, tenantID string, lease vmlease.Lease) (*vmlease.Lease, error) {
	return s.store.Update(ctx, tenantID, lease)
}

func TestServiceInventoryFailsClosedWithoutAuthorityAwareStore(t *testing.T) {
	service := NewService(inventoryBlindStore{store: NewMemoryStore()}, ServiceConfig{})
	if _, err := service.ListInventoryByTenant(context.Background(), "org-1"); !errors.Is(err, ErrLeaseInventoryUnavailable) {
		t.Fatalf("ListInventoryByTenant error = %v, want unavailable", err)
	}
	if _, err := service.GetInventory(context.Background(), "org-1", vmlease.LeaseID("lease-1")); !errors.Is(err, ErrLeaseInventoryUnavailable) {
		t.Fatalf("GetInventory error = %v, want unavailable", err)
	}
}

var _ Store = inventoryBlindStore{}
