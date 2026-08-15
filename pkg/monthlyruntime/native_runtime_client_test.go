package monthlyruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

func TestNativeRuntimeClientProjectsProviderAndGuardTruth(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	leaseID := "lease-1"
	serverID := runtimeidentity.LeaseServerID(leaseID)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: serverID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", LeaseID: leaseID,
		ProviderRef: "ionos", LifecycleState: "active", DesiredState: "running",
		ConnectionState: "connected", HealthState: "healthy", InventoryRevision: 42,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	client := &NativeRuntimeClient{Servers: store}
	response, err := client.RuntimeAction(t.Context(), serverruntime.LeaseRuntimeActionRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", LeaseID: leaseID,
		Action: serverruntime.RuntimeActionStatus, Metadata: map[string]string{"runtime_public_ip": "192.0.2.10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ProviderID != "ionos" || response.ObservedState != "running" || response.Status == nil ||
		response.Status.PublicIP != "192.0.2.10" || response.Metadata["runtime_reason_code"] != "provider_and_guard_healthy" ||
		response.Metadata["inventory_revision"] != "42" {
		t.Fatalf("response = %+v", response)
	}
}

func TestNativeRuntimeClientFailsClosedForCrossTenantAndUnsupportedAction(t *testing.T) {
	store := controlplane.NewMemoryStore()
	leaseID := "lease-1"
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: runtimeidentity.LeaseServerID(leaseID), TenantID: "tenant-1", OwnerSubjectID: "owner-1",
		LeaseID: leaseID, ProviderRef: "centron", LifecycleState: "active",
	}); err != nil {
		t.Fatal(err)
	}
	client := &NativeRuntimeClient{Servers: store}
	_, err := client.RuntimeAction(t.Context(), serverruntime.LeaseRuntimeActionRequest{
		TenantID: "tenant-2", OwnerID: "owner-1", LeaseID: leaseID, Action: serverruntime.RuntimeActionStatus,
	})
	if !errors.Is(err, ErrNativeRuntimeObservationUnavailable) {
		t.Fatalf("cross-tenant error = %v", err)
	}
	_, err = client.RuntimeAction(t.Context(), serverruntime.LeaseRuntimeActionRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", LeaseID: leaseID, Action: serverruntime.RuntimeActionStart,
	})
	if !errors.Is(err, ErrNativeRuntimeActionUnsupported) {
		t.Fatalf("unsupported action error = %v", err)
	}
	var unsupported *NativeRuntimeActionUnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Action != serverruntime.RuntimeActionStart {
		t.Fatalf("unsupported action details = %#v", unsupported)
	}
	details := NativeRuntimeActionUnsupportedDetails(err)
	if details["error_code"] != NativeRuntimeActionUnsupportedErrorCode ||
		details["reason_code"] != "provider_recreate_not_ready" {
		t.Fatalf("unsupported action details = %#v", details)
	}
}

func TestNativeRuntimeStopGuidanceDoesNotClaimProviderPause(t *testing.T) {
	err := &NativeRuntimeActionUnsupportedError{Action: serverruntime.RuntimeActionStop}
	details := NativeRuntimeActionUnsupportedDetails(err)
	if details["reason_code"] != "provider_pause_unsupported" || details["retryable"] != false {
		t.Fatalf("stop details = %#v", details)
	}
}

func TestNativeObservedStateDoesNotClaimHealthyWithoutBothAuthorities(t *testing.T) {
	state, reason := nativeObservedState(controlplane.ServerRuntime{
		LifecycleState: "active", ConnectionState: "offline", HealthState: "healthy",
	})
	if state != "offline" || reason != "guard_connection_offline" {
		t.Fatalf("state=%q reason=%q", state, reason)
	}
}
