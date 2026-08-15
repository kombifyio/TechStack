package runtimelease

import (
	"errors"
	"testing"
	"time"
)

func TestLeaseValidate(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 30, 0, 0, time.UTC)
	lease := Lease{
		ID:                   "lease-compat",
		Revision:             7,
		TenantID:             "tenant-compat",
		OwnerID:              "owner-compat",
		ServerID:             "server-compat",
		ResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000",
		DesiredState:         DesiredStateRunning,
		ValidFrom:            now.Add(-30 * time.Minute),
		ValidUntil:           now.Add(30 * time.Minute),
	}
	if err := lease.Validate(now); err != nil {
		t.Fatalf("valid self-host lease rejected: %v", err)
	}

	lease.CancelledAt = ptr(now)
	if err := lease.Validate(now); !errors.Is(err, ErrLeaseCancelled) {
		t.Fatalf("cancelled self-host lease error = %v, want %v", err, ErrLeaseCancelled)
	}
}

func ptr(value time.Time) *time.Time {
	return &value
}
