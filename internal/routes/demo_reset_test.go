package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const demoResetTestTenant = "demo-tenant"

type demoResetJobEnqueuer struct {
	requests []monthlyruntime.ReconciliationRequest
	err      error
	ready    bool
}

type demoResetInventoryLeaseService struct {
	*vmleases.Service
	records []vmleases.LeaseInventoryRecord
}

func (s *demoResetInventoryLeaseService) ListInventoryByTenant(
	_ context.Context,
	_ string,
) ([]vmleases.LeaseInventoryRecord, error) {
	return append([]vmleases.LeaseInventoryRecord(nil), s.records...), nil
}

func (f *demoResetJobEnqueuer) EnqueueProviderReconciliation(
	_ context.Context,
	req monthlyruntime.ReconciliationRequest,
) error {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return f.err
	}
	return nil
}

func (f *demoResetJobEnqueuer) DurableReconciliationReady() bool { return f != nil && f.ready }

func seedDemoResetLease(t *testing.T, leases *vmleases.Service, id, stackID string, enrolledAt time.Time, protected bool) {
	t.Helper()
	metadata := monthlyruntime.NormalizeMetadata(map[string]string{
		"stack_id":                  stackID,
		"runtime_enrollment_status": "enrolled",
		"enrollment_started_at":     enrolledAt.UTC().Format(time.RFC3339),
	}, serverruntime.RuntimeOfferingStandard)
	_ = protected // protection rides in the env var, not the lease record
	lease := vmlease.Lease{
		ID:             vmlease.LeaseID(id),
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: "demo-user", OrgID: demoResetTestTenant},
		Resource:       vmlease.ResourceRef{ProviderID: monthlyruntime.ProviderCentron, EngineVMID: "engine-" + id},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      enrolledAt,
		ValidUntil:     enrolledAt.Add(30 * 24 * time.Hour),
		RenewedAt:      enrolledAt,
		Metadata:       metadata,
	}
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("seed lease %s: %v", id, err)
	}
}

func demoResetTestHandlers(
	t *testing.T,
	leases *vmleases.Service,
	enqueuer *demoResetJobEnqueuer,
	stacks controlplane.StackStore,
	workers controlplane.WorkerStore,
) demoResetHandlers {
	t.Helper()
	authority := &nativeTestLeaseService{Service: leases}
	if enqueuer == nil {
		enqueuer = &demoResetJobEnqueuer{ready: true}
	}
	var jobStore controlplane.JobStore
	var registryStore controlplane.ServerRuntimeStore
	if store, ok := stacks.(controlplane.JobStore); ok {
		jobStore = store
	}
	if store, ok := stacks.(controlplane.ServerRuntimeStore); ok {
		registryStore = store
	}
	return demoResetHandlers{
		leases:     authority,
		reconciler: enqueuer,
		stacks: StackLifecycleStores{
			Stacks: stacks, Workers: workers, Jobs: jobStore, Registry: registryStore,
		},
	}
}

func decodeDemoResetResponse(t *testing.T, body []byte) demoResetResponse {
	t.Helper()
	var envelope struct {
		Data demoResetResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode reset response: %v (%s)", err, string(body))
	}
	return envelope.Data
}

func TestDemoResetFailsClosedWithoutConfig(t *testing.T) {
	t.Setenv(envDemoResetSecret, "")
	t.Setenv(demoguard.EnvDemoTenantID, "")
	h := demoResetTestHandlers(t, vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")}), nil, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without config", recorder.Code)
	}
}

func TestDemoResetRejectsWrongSecret(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	h := demoResetTestHandlers(t, vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")}), nil, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "wrong")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 on wrong secret", recorder.Code)
	}
}

func TestDemoResetFailsClosedWithoutDurableProviderControl(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	h := demoResetTestHandlers(t, leases, &demoResetJobEnqueuer{ready: false}, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 without durable provider-control custody", recorder.Code)
	}
}

func TestDemoResetSurfacesLegacyCustodyResolutionWithoutEnqueueing(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "")

	now := time.Now().UTC()
	base := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	seedDemoResetLease(t, base, "lease-legacy", "stack-legacy", now.Add(-2*time.Hour), false)
	lease, err := base.Get(context.Background(), demoResetTestTenant, "lease-legacy")
	if err != nil {
		t.Fatalf("Get lease: %v", err)
	}
	legacy := &demoResetInventoryLeaseService{
		Service: base,
		records: []vmleases.LeaseInventoryRecord{{
			Lease:          *lease,
			AuthorityState: vmleases.LeaseAuthorityStateUnbound,
		}},
	}
	enqueuer := &demoResetJobEnqueuer{ready: true}
	h := demoResetHandlers{
		leases:     legacy,
		reconciler: enqueuer,
	}
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	resp := decodeDemoResetResponse(t, recorder.Body.Bytes())
	if len(resp.ResolutionRequired) != 1 {
		t.Fatalf("resolution_required = %#v, want one exact action", resp.ResolutionRequired)
	}
	resolution := resp.ResolutionRequired[0]
	if resolution.LeaseID != "lease-legacy" || resolution.Action != "resolve_custody" ||
		resolution.Endpoint != "/api/v1/monthly-runtimes/lease-legacy/resolve-custody" {
		t.Fatalf("resolution = %#v, want exact resolve-custody action", resolution)
	}
	if len(enqueuer.requests) != 0 {
		t.Fatalf("provider reconciliation requests = %d, want none for unbound custody", len(enqueuer.requests))
	}
	stored, err := base.Get(context.Background(), demoResetTestTenant, "lease-legacy")
	if err != nil {
		t.Fatalf("Get stored lease: %v", err)
	}
	if stored.CancelledAt != nil || stored.DesiredState == vmlease.DesiredStateArchived {
		t.Fatal("reset archived an unbound lease without provider-removal confirmation")
	}
}

func TestDemoResetDecommissionsOnlyUnprotectedPastGrace(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "lease-anchor-main,lease-anchor-worker")

	now := time.Now().UTC()
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	seedDemoResetLease(t, leases, "lease-anchor-main", "stack-demo", now.Add(-48*time.Hour), true)
	seedDemoResetLease(t, leases, "lease-anchor-worker", "stack-demo", now.Add(-48*time.Hour), true)
	seedDemoResetLease(t, leases, "lease-visitor-old", "stack-visitor", now.Add(-2*time.Hour), false)
	seedDemoResetLease(t, leases, "lease-visitor-young", "stack-young", now.Add(-5*time.Minute), false)

	stacks := controlplane.NewMemoryStore()
	for _, id := range []string{"stack-demo", "stack-visitor", "stack-young"} {
		if _, err := stacks.CreateStack(context.Background(), controlplane.CreateStackRequest{
			ID: id, TenantID: demoResetTestTenant, OwnerSubjectID: "demo-user", Name: id, Status: "running",
		}); err != nil {
			t.Fatalf("seed stack %s: %v", id, err)
		}
	}

	enqueuer := &demoResetJobEnqueuer{ready: true}
	h := demoResetTestHandlers(t, leases, enqueuer, stacks, stacks)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	resp := decodeDemoResetResponse(t, recorder.Body.Bytes())
	if strings.Contains(recorder.Body.String(), "scheduled_jobs") ||
		strings.Contains(recorder.Body.String(), `"scheduled"`) {
		t.Fatal("demo reset claimed a generic queue job instead of durable provider-control custody")
	}
	if len(resp.Admitted) != 1 || resp.Admitted[0] != "lease-visitor-old" {
		t.Fatalf("admitted = %v, want exactly lease-visitor-old", resp.Admitted)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0] != "lease-visitor-old" {
		t.Fatalf("candidates = %v, want exactly lease-visitor-old", resp.Candidates)
	}
	if resp.SkippedProtected != 2 {
		t.Fatalf("skipped_protected = %d, want 2", resp.SkippedProtected)
	}
	if resp.SkippedGrace != 1 {
		t.Fatalf("skipped_grace = %d, want 1", resp.SkippedGrace)
	}
	for _, id := range []string{"lease-anchor-main", "lease-anchor-worker", "lease-visitor-young"} {
		stored, err := leases.Get(context.Background(), demoResetTestTenant, vmlease.LeaseID(id))
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if stored.CancelledAt != nil {
			t.Fatalf("lease %s was canceled but must survive the reset", id)
		}
	}
	victim, err := leases.Get(context.Background(), demoResetTestTenant, "lease-visitor-old")
	if err != nil {
		t.Fatalf("Get victim: %v", err)
	}
	if victim.CancelledAt != nil {
		t.Fatal("reset endpoint canceled the victim before durable provider reconciliation")
	}
	if len(enqueuer.requests) != 1 {
		t.Fatalf("reconciliation requests = %d, want 1", len(enqueuer.requests))
	}
	request := enqueuer.requests[0]
	if request.StackID != "stack-visitor" || request.TenantID != demoResetTestTenant ||
		request.OwnerID != "demo-user" || request.LeaseID != "lease-visitor-old" ||
		len(request.ResourceGenerationDigest) != 64 || request.Reason != "demo_reset_expired_lease" {
		t.Fatalf("reconciliation request = %+v", request)
	}
	if _, err := stacks.GetStack(context.Background(), demoResetTestTenant, "stack-visitor"); err != nil {
		t.Fatalf("scheduled stack must remain until provider proof: %v", err)
	}
}

func TestDemoResetReportsDurableAdmissionFailureWithoutCancelling(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "")

	now := time.Now().UTC()
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	seedDemoResetLease(t, leases, "lease-halfdone", "stack-halfdone", now.Add(-2*time.Hour), false)

	enqueuer := &demoResetJobEnqueuer{ready: true, err: errors.New("durable queue unavailable")}
	h := demoResetTestHandlers(t, leases, enqueuer, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	resp := decodeDemoResetResponse(t, recorder.Body.Bytes())
	if len(resp.Candidates) != 1 || len(resp.Admitted) != 0 || len(resp.Warnings) != 1 {
		t.Fatalf("response = candidates %v admitted %v warnings %v, want one candidate and one honest admission failure", resp.Candidates, resp.Admitted, resp.Warnings)
	}
	stored, err := leases.Get(context.Background(), demoResetTestTenant, "lease-halfdone")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("lease was canceled without a durable cleanup job")
	}
}

func TestDemoResetDryRunMutatesNothing(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "")

	now := time.Now().UTC()
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	seedDemoResetLease(t, leases, "lease-visitor-old", "stack-visitor", now.Add(-2*time.Hour), false)

	enqueuer := &demoResetJobEnqueuer{ready: true}
	h := demoResetTestHandlers(t, leases, enqueuer, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset?dry_run=true", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	resp := decodeDemoResetResponse(t, recorder.Body.Bytes())
	if !resp.DryRun {
		t.Fatal("dry_run flag not reflected")
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0] != "lease-visitor-old" {
		t.Fatalf("dry-run candidates = %v, want lease-visitor-old", resp.Candidates)
	}
	if len(resp.Admitted) != 0 {
		t.Fatalf("dry-run admitted = %v, want no claimed mutation", resp.Admitted)
	}
	if len(enqueuer.requests) != 0 {
		t.Fatalf("queue saw %d requests in dry-run, want none", len(enqueuer.requests))
	}
	stored, err := leases.Get(context.Background(), demoResetTestTenant, "lease-visitor-old")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.CancelledAt != nil {
		t.Fatal("dry-run canceled a lease")
	}
}

func TestDemoResetAdmitsAllEligibleWithinDefaultBatch(t *testing.T) {
	t.Setenv(envDemoResetSecret, "topsecret")
	t.Setenv(demoguard.EnvDemoTenantID, demoResetTestTenant)
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "")

	now := time.Now().UTC()
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{SnapshotSecret: []byte("s")})
	ids := []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7"}
	for _, id := range ids {
		seedDemoResetLease(t, leases, id, "stack-"+id, now.Add(-3*time.Hour), false)
	}

	enqueuer := &demoResetJobEnqueuer{ready: true}
	h := demoResetTestHandlers(t, leases, enqueuer, nil, nil)
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/internal/demo/reset", "", "", "")
	event.Request.Header.Set(demoResetSecretHeader, "topsecret")
	if err := h.reset(event); err != nil {
		t.Fatalf("reset returned router error: %v", err)
	}
	resp := decodeDemoResetResponse(t, recorder.Body.Bytes())
	if len(resp.Admitted) != len(ids) {
		t.Fatalf("admitted = %d, want all %d eligible leases", len(resp.Admitted), len(ids))
	}
	if len(resp.Candidates) != len(ids) {
		t.Fatalf("candidates = %d, want all %d eligible leases", len(resp.Candidates), len(ids))
	}
	if resp.SkippedCap != 0 {
		t.Fatalf("skipped_cap = %d, want 0 within the default batch", resp.SkippedCap)
	}
}

func TestDemoResetRotatesBoundedBatchAcrossStableLeasePopulation(t *testing.T) {
	t.Setenv(envDemoResetGraceMinutes, "0")
	t.Setenv(demoguard.EnvDemoProtectedLeaseIDs, "")
	now := time.Unix(0, 0).UTC()
	leases := make([]vmlease.Lease, 0, 7)
	for _, id := range []string{"l1", "l2", "l3", "l4", "l5", "l6", "l7"} {
		leases = append(leases, vmlease.Lease{
			ID:           vmlease.LeaseID(id),
			DesiredState: vmlease.DesiredStateRunning,
			Metadata: monthlyruntime.NormalizeMetadata(map[string]string{
				"runtime_offering":          string(serverruntime.RuntimeOfferingStandard),
				"runtime_enrollment_status": "enrolled",
				"enrollment_started_at":     now.Add(-2 * time.Hour).Format(time.RFC3339),
			}, serverruntime.RuntimeOfferingStandard),
		})
	}

	firstResp := demoResetResponse{}
	first := selectDemoResetVictimsAt(leases, &firstResp, now, 3)
	if got := demoResetLeaseIDs(first); !reflect.DeepEqual(got, []string{"l1", "l2", "l3"}) {
		t.Fatalf("first batch = %v, want l1-l3", got)
	}
	if firstResp.SkippedCap != 4 {
		t.Fatalf("first skipped_cap = %d, want 4", firstResp.SkippedCap)
	}

	secondResp := demoResetResponse{}
	second := selectDemoResetVictimsAt(leases, &secondResp, now.Add(demoResetRotationPeriod), 3)
	if got := demoResetLeaseIDs(second); !reflect.DeepEqual(got, []string{"l4", "l5", "l6"}) {
		t.Fatalf("second batch = %v, want l4-l6", got)
	}
	if secondResp.SkippedCap != 4 {
		t.Fatalf("second skipped_cap = %d, want 4", secondResp.SkippedCap)
	}
}

func TestDemoResetBatchSizeIsBoundedAndConfigurable(t *testing.T) {
	t.Setenv(envDemoResetBatchSize, "3")
	if got := demoResetBatchSize(); got != 3 {
		t.Fatalf("configured batch size = %d, want 3", got)
	}
	t.Setenv(envDemoResetBatchSize, "999999")
	if got := demoResetBatchSize(); got != maxDemoResetBatchSize {
		t.Fatalf("oversized batch size = %d, want hard maximum %d", got, maxDemoResetBatchSize)
	}
	t.Setenv(envDemoResetBatchSize, "invalid")
	if got := demoResetBatchSize(); got != defaultDemoResetBatchSize {
		t.Fatalf("invalid batch size = %d, want default %d", got, defaultDemoResetBatchSize)
	}
}

func demoResetLeaseIDs(leases []vmlease.Lease) []string {
	ids := make([]string, 0, len(leases))
	for _, lease := range leases {
		ids = append(ids, string(lease.ID))
	}
	return ids
}

func TestDemoResetPruneWaitsForTerminalCleanupJob(t *testing.T) {
	for _, state := range []string{"pending", "running", "waiting"} {
		if !demoStackHasUnsafeCleanupJob([]controlplane.Job{{State: state}}) {
			t.Fatalf("state %q did not fence orphan pruning", state)
		}
	}
	for _, state := range []string{"failed", "canceled", "cancelled"} {
		if !demoStackHasUnsafeCleanupJob([]controlplane.Job{{Type: "reconcile_lease", State: state}}) {
			t.Fatalf("unsafe cleanup state %q did not fence orphan pruning", state)
		}
	}
	if demoStackHasUnsafeCleanupJob([]controlplane.Job{
		{Type: "reconcile_lease", State: "completed"},
		{Type: "provision", State: "failed"},
		{Type: "update", State: "canceled"},
	}) {
		t.Fatal("safe terminal job history permanently fenced orphan pruning")
	}
}

func TestDemoResetPruneRequiresTerminalServerRegistry(t *testing.T) {
	for _, lifecycle := range []string{"planned", "provisioning", "active", "failed", "decommissioning"} {
		if !demoStackHasNonTerminalServer([]controlplane.ServerRuntime{{LifecycleState: lifecycle}}) {
			t.Fatalf("lifecycle %q did not fence orphan pruning", lifecycle)
		}
	}
	if demoStackHasNonTerminalServer([]controlplane.ServerRuntime{{
		LifecycleState: "decommissioned",
	}}) {
		t.Fatal("terminal RuntimeServer tombstone fenced orphan pruning")
	}
}
