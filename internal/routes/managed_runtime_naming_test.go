package routes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// TestManagedRuntimeNodeHostnameNeverBareBrand pins the collision fix: a single
// node must never carry the platform brand name ("techstack" = the WHOLE
// homelab), and must be distinct from the bare stack name.
func TestManagedRuntimeNodeHostnameNeverBareBrand(t *testing.T) {
	// Stack named the platform brand -> node must NOT be "techstack".
	got := managedRuntimeNodeHostname(
		map[string]string{"stack_name": "techstack", "role": "foundation"},
		vmlease.LeaseID("lease-qc4h0d13o125z6d"),
	)
	if IsReservedPlatformNodeName(got) {
		t.Fatalf("node hostname is a reserved brand name: %q", got)
	}
	if !IsReservedPlatformNodeName("techstack-3") {
		t.Fatal("techstack-* names must be reserved for platform naming, not node naming")
	}
	if prefixed := managedRuntimeNodeHostname(
		map[string]string{"stack_name": "techstack-3", "role": "worker"},
		vmlease.LeaseID("lease-prefixed123"),
	); strings.Contains(prefixed, "techstack") || IsReservedPlatformNodeName(prefixed) {
		t.Fatalf("prefixed platform stack name leaked into node hostname: %q", prefixed)
	}

	// A distinct stack name must not be used bare as the node name.
	if h := managedRuntimeNodeHostname(
		map[string]string{"stack_name": "myhomelab", "role": "worker"},
		vmlease.LeaseID("lease-abcdef123"),
	); h == "myhomelab" {
		t.Fatalf("node hostname equals the bare stack name: %q", h)
	}

	// An explicit reported hostname wins...
	if h := managedRuntimeNodeHostname(
		map[string]string{workerFieldHostname: "vm-123"},
		vmlease.LeaseID("lease-x"),
	); h != "vm-123" {
		t.Fatalf("explicit reported hostname not honored: %q", h)
	}

	// ...unless it is itself a reserved brand name, which must be replaced.
	if h := managedRuntimeNodeHostname(
		map[string]string{workerFieldHostname: "TechStack", "role": "foundation"},
		vmlease.LeaseID("lease-qc4h0d"),
	); IsReservedPlatformNodeName(h) {
		t.Fatalf("reserved reported hostname was not sanitized: %q", h)
	}
}

// TestManagedRuntimeHealthStateNotFakeHealthy pins C1: a managed lease
// projection has no telemetry agent, so it must not claim measured "healthy".
func TestManagedRuntimeHealthStateNotFakeHealthy(t *testing.T) {
	lease := vmlease.Lease{DesiredState: vmlease.DesiredStateRunning}
	got := managedRuntimeHealthState(lease, monthlyRuntimeEnrollmentStatusEnrolled, "203.0.113.10", 0)
	if got == "healthy" {
		t.Fatalf("managed projection must not report measured 'healthy' without telemetry; got %q", got)
	}
	if got != "provisioned" {
		t.Fatalf("want 'provisioned' for enrolled+reachable-without-telemetry, got %q", got)
	}
}

// TestManagedRuntimeHealthStateEquivalence pins that projecting through the
// lifecycle state machine preserves the historical dashboard status strings.
func TestManagedRuntimeHealthStateEquivalence(t *testing.T) {
	running := vmlease.Lease{DesiredState: vmlease.DesiredStateRunning}
	stopped := vmlease.Lease{DesiredState: vmlease.DesiredStateStopped}
	cases := []struct {
		name   string
		lease  vmlease.Lease
		status string
		target string
		want   string
	}{
		{"enrolled+target", running, monthlyRuntimeEnrollmentStatusEnrolled, "203.0.113.10", "provisioned"},
		{"enrolled+no-target", running, monthlyRuntimeEnrollmentStatusEnrolled, "", "pending"},
		{"enrolled+stopped", stopped, monthlyRuntimeEnrollmentStatusEnrolled, "203.0.113.10", "offline"},
		{"failed", running, monthlyRuntimeEnrollmentStatusFailed, "", "error"},
		{"retrying", running, monthlyRuntimeEnrollmentStatusRetrying, "", "stale"},
		{"pending", running, monthlyRuntimeEnrollmentStatusPending, "", "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedRuntimeHealthState(tc.lease, tc.status, tc.target, 0); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestManagedRuntimeHealthStateStalled pins the new stalled projection: an
// enrollment pending past the threshold surfaces as "stalled", fresh stays
// "pending".
func TestManagedRuntimeHealthStateStalled(t *testing.T) {
	lease := vmlease.Lease{DesiredState: vmlease.DesiredStateRunning}
	if got := managedRuntimeHealthState(lease, monthlyRuntimeEnrollmentStatusPending, "", monthlyruntime.EnrollmentStalledThreshold+time.Minute); got != "stalled" {
		t.Fatalf("want 'stalled' for pending past threshold, got %q", got)
	}
	if got := managedRuntimeHealthState(lease, monthlyRuntimeEnrollmentStatusPending, "", time.Minute); got != "pending" {
		t.Fatalf("want 'pending' for fresh pending, got %q", got)
	}
}

// TestManagedRuntimeEnrollmentAge covers the enrollment_started_at parsing and
// its fail-safe zero when unstamped.
func TestManagedRuntimeEnrollmentAge(t *testing.T) {
	if age := managedRuntimeEnrollmentAge(map[string]string{}); age != 0 {
		t.Fatalf("unstamped age = %s, want 0", age)
	}
	if age := managedRuntimeEnrollmentAge(map[string]string{"enrollment_started_at": "not-a-time"}); age != 0 {
		t.Fatalf("unparseable age = %s, want 0", age)
	}
	past := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	if age := managedRuntimeEnrollmentAge(map[string]string{"enrollment_started_at": past}); age < 25*time.Minute {
		t.Fatalf("stamped age = %s, want >= 25m", age)
	}
}

func TestManagedRuntimeProjectionIncludesPersistedAccessContext(t *testing.T) {
	lease := createStackOperationsTestLease("lease-1", "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled)
	lease.Metadata["runtime_ssh_host"] = "203.0.113.10"
	lease.Metadata["runtime_ssh_user"] = "ubuntu"
	lease.Metadata["runtime_ssh_port"] = "2222"

	server := stackServerFromManagedRuntime(managedRuntimeInventoryItemFromLease(nativeActiveManagedRuntimeRecord(lease)))
	if server.Capabilities["runtime_ssh_host"] != "203.0.113.10" {
		t.Fatalf("runtime_ssh_host not projected: %#v", server.Capabilities)
	}
	if server.Capabilities["runtime_ssh_user"] != "ubuntu" {
		t.Fatalf("runtime_ssh_user not projected: %#v", server.Capabilities)
	}
	if server.Capabilities["runtime_ssh_port"] != 2222 {
		t.Fatalf("runtime_ssh_port not projected: %#v", server.Capabilities)
	}
}

func TestProjectManagedRuntimeLeasesKeepsCanceledInventoryVisible(t *testing.T) {
	now := time.Date(2026, 7, 3, 16, 45, 0, 0, time.UTC)
	active := createStackOperationsTestLease("lease-active", "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled)
	canceled := createStackOperationsTestLease("lease-canceled", "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled)
	canceled.CancelledAt = &now
	canceled.DesiredState = vmlease.DesiredStateStopped

	items, err := projectManagedRuntimeLeasesChecked(
		context.Background(),
		fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{active, canceled}},
		"tenant-1",
		"owner-1",
		"stack-1",
	)
	if err != nil {
		t.Fatalf("project managed runtime leases: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("projected leases = %d, want 2: %#v", len(items), items)
	}
	if items[0].LeaseID != "lease-active" || items[1].LeaseID != "lease-canceled" || items[1].Status != "inactive" || items[1].Approved {
		t.Fatalf("projected inventory = %#v, canceled lease must remain visible and blocked", items)
	}
}

func TestProjectManagedRuntimeLeasesKeepsArchivedLegacyCustodyVisible(t *testing.T) {
	lease := createStackOperationsTestLease("lease-legacy", "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled)
	lease.DesiredState = vmlease.DesiredStateArchived
	record := vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate,
		AuthorityState:     vmleases.LeaseAuthorityStateLegacyQuarantined,
	}
	items, err := projectManagedRuntimeLeasesChecked(t.Context(), fakeManagedRuntimeLeaseLister{inventory: []vmleases.LeaseInventoryRecord{record}}, "tenant-1", "owner-1", "stack-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("projection = %#v, err = %v", items, err)
	}
	if items[0].Status != managedRuntimeStatusQuarantined || items[0].Approved || items[0].NativeActive {
		t.Fatalf("archived legacy custody was hidden or actionable: %#v", items[0])
	}
}

func TestManagedRuntimeProjectionKeepsNonNativeAuthorityVisibleButBlocked(t *testing.T) {
	for _, test := range []struct {
		name       string
		authority  vmleases.LeaseExecutionAuthority
		state      vmleases.LeaseAuthorityState
		wantStatus string
	}{
		{name: "legacy", authority: vmleases.LeaseExecutionAuthorityLegacySimulate, state: vmleases.LeaseAuthorityStateLegacyQuarantined, wantStatus: managedRuntimeStatusQuarantined},
		{name: "unbound", state: vmleases.LeaseAuthorityStateUnbound, wantStatus: "unbound"},
		{name: "native inactive", authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl, state: vmleases.LeaseAuthorityStateNativeInactive, wantStatus: "inactive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := createStackOperationsTestLease("lease-1", "tenant-1", "owner-1", "stack-1", monthlyRuntimeEnrollmentStatusEnrolled)
			record := vmleases.LeaseInventoryRecord{Lease: lease, ExecutionAuthority: test.authority, AuthorityState: test.state}
			items, err := projectManagedRuntimeLeasesChecked(t.Context(), fakeManagedRuntimeLeaseLister{inventory: []vmleases.LeaseInventoryRecord{record}}, "tenant-1", "owner-1", "stack-1")
			if err != nil || len(items) != 1 {
				t.Fatalf("projection = %#v, err = %v", items, err)
			}
			item := items[0]
			if item.Status != test.wantStatus || item.Approved || item.NativeActive {
				t.Fatalf("non-native item = %#v, want status %q and blocked", item, test.wantStatus)
			}
			server := stackServerFromManagedRuntime(item)
			overlayManagedRuntimeMetrics(&server, func(string) (float64, bool) { return 42, true })
			if server.Status != test.wantStatus || server.Approved || server.Assignable || server.Health.State == "healthy" || server.Health.State == managedRuntimeStatusProvisioned {
				t.Fatalf("non-native server became actionable/healthy: %#v", server)
			}
		})
	}
}

func TestManagedRuntimeAuthorityOverridesPersistedHealthyWorker(t *testing.T) {
	servers := []stackOperationServer{{
		ID:         "worker-1",
		LeaseID:    "lease-1",
		Status:     "healthy",
		Approved:   true,
		Assignable: true,
		Health:     stackServerHealth{State: "healthy"},
	}}
	applyManagedRuntimeAuthorityToServers(servers, []managedRuntimeInventoryItem{{
		LeaseID:        "lease-1",
		Status:         managedRuntimeStatusQuarantined,
		AuthorityState: string(vmleases.LeaseAuthorityStateLegacyQuarantined),
	}})
	server := servers[0]
	if server.Status != managedRuntimeStatusQuarantined || server.Health.State != managedRuntimeStatusQuarantined || server.Approved || server.Assignable {
		t.Fatalf("persisted worker bypassed authority projection: %#v", server)
	}
}
