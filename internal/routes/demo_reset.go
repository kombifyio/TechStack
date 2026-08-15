package routes

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/pocketbase/pocketbase/core"
)

const (
	// envDemoResetSecret authenticates the internal reset endpoint (header
	// X-Kombify-Demo-Reset-Secret, constant-time compare).
	envDemoResetSecret     = "TECHSTACK_DEMO_RESET_SECRET"      //nolint:gosec // env var NAME, not a credential value
	envDemoResetSecretNext = "TECHSTACK_DEMO_RESET_SECRET_NEXT" //nolint:gosec // env var NAME, not a credential value
	// envDemoResetGraceMinutes protects a visitor mid-demo: leases younger than
	// the grace window are never reaped. Default 45.
	envDemoResetGraceMinutes = "TECHSTACK_DEMO_RESET_GRACE_MINUTES"
	// envDemoResetBatchSize bounds the number of cleanup admissions attempted by
	// one reset run. The default is large enough for normal demo churn while the
	// hard maximum keeps a malformed configuration from turning the endpoint
	// into an unbounded provider fan-out.
	envDemoResetBatchSize        = "TECHSTACK_DEMO_RESET_BATCH_SIZE"
	defaultDemoResetGraceMinutes = 45
	defaultDemoResetBatchSize    = 250
	maxDemoResetBatchSize        = 500
	// A scheduled reset normally runs once per day. Advancing the rotation at
	// this cadence makes each run pick the next bounded slice, so an admission
	// failure in the first slice cannot starve every later lease forever.
	demoResetRotationPeriod = 24 * time.Hour
	demoResetSecretHeader   = "X-Kombify-Demo-Reset-Secret" //nolint:gosec // header NAME, not a credential value
)

type demoResetHandlers struct {
	app        core.App
	leases     stackLifecycleLeaseService
	reconciler monthlyruntime.ReconciliationEnqueuer
	stacks     StackLifecycleStores
}

// demoResetLeaseInventoryReader is the authority-aware extension implemented
// by the canonical Postgres lease service. The reset route still accepts the
// smaller lifecycle lease seam for compatibility, but it must not infer
// provider custody from a lease row alone.
type demoResetLeaseInventoryReader interface {
	ListInventoryByTenant(context.Context, string) ([]vmleases.LeaseInventoryRecord, error)
}

type demoResetCustodyResolution struct {
	LeaseID  string `json:"lease_id"`
	Action   string `json:"action,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Reason   string `json:"reason"`
}

type demoResetResponse struct {
	Message            string                       `json:"message"`
	DryRun             bool                         `json:"dry_run"`
	Candidates         []string                     `json:"candidates"`
	Admitted           []string                     `json:"admitted"`
	PrunedStacks       int                          `json:"pruned_stacks"`
	SkippedProtected   int                          `json:"skipped_protected"`
	SkippedGrace       int                          `json:"skipped_grace"`
	SkippedCap         int                          `json:"skipped_cap"`
	ResolutionRequired []demoResetCustodyResolution `json:"resolution_required,omitempty"`
	Warnings           []string                     `json:"warnings,omitempty"`
}

// RegisterDemoResetRoutes wires the internal demo-slot reset used by the
// public live demo (nightly cron). It admits exact-generation cleanup through
// the native provider-control application for every eligible lease and prunes
// only stacks whose durable cleanup work is terminally successful. SaaS only;
// fail-closed without env config.
func RegisterDemoResetRoutes(
	r *httpx.Router,
	app core.App,
	leases stackLifecycleLeaseService,
	reconciler monthlyruntime.ReconciliationEnqueuer,
	stores StackLifecycleStores,
) {
	if r == nil {
		return
	}
	h := demoResetHandlers{app: app, leases: leases, reconciler: reconciler, stacks: stores}
	r.POST("/api/internal/demo/reset", h.reset)
}

func (h demoResetHandlers) reset(e *httpx.Event) error {
	// The authorizer writes its own rejection envelope, and httpx.Error returns
	// nil once that write succeeds, so a refused request arrives here with a nil
	// error and an empty tenant. Branching on the error alone let a refused
	// caller fall through into this destructive handler.
	tenantID, authErr := h.authorizeDemoReset(e)
	if authErr != nil || tenantID == "" {
		return authErr
	}
	dryRun := demoResetDryRunRequested(e)

	ctx := e.Request.Context()
	resp := demoResetResponse{
		Message:    "Demo cleanup admission",
		DryRun:     dryRun,
		Candidates: []string{},
		Admitted:   []string{},
		Warnings:   []string{},
	}

	leases, err := h.leases.ListByTenant(ctx, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to list demo tenant leases", map[string]any{"error": err.Error()})
	}
	victims := selectDemoResetVictims(leases, &resp)
	inventory, inventoryErr := demoResetLeaseInventory(ctx, h.leases, tenantID)

	for _, lease := range victims {
		resp.Candidates = append(resp.Candidates, string(lease.ID))
		if inventoryErr != nil {
			resp.ResolutionRequired = append(resp.ResolutionRequired,
				demoResetCustodyReviewFor(lease, "authority-aware lease inventory is unavailable"))
			continue
		}
		record, ok := inventory[lease.ID]
		if !ok {
			resp.ResolutionRequired = append(resp.ResolutionRequired,
				demoResetCustodyReviewFor(lease, "lease authority record is missing; provider absence cannot be inferred"))
			continue
		}
		if record.ExecutionAuthority != vmleases.LeaseExecutionAuthorityTechStackProviderControl {
			// Legacy and unbound rows have no provider-control custody. They are
			// intentionally not auto-archived: a missing native row is not proof
			// that a billable provider resource is absent. The normal operations
			// surface already exposes the exact owner-confirmed resolution action.
			resp.ResolutionRequired = append(resp.ResolutionRequired,
				demoResetCustodyResolutionFor(lease, "legacy or unbound custody requires provider-removal confirmation"))
			continue
		}
		if dryRun {
			continue
		}
		admissionErr := h.admitDemoLeaseCleanup(ctx, tenantID, lease)
		if admissionErr != nil {
			resp.Warnings = append(resp.Warnings, string(lease.ID)+": "+admissionErr.Error())
			continue
		}
		resp.Admitted = append(resp.Admitted, string(lease.ID))
	}

	if !dryRun {
		pruned, warnings := h.pruneDemoOrphanStacks(ctx, tenantID)
		resp.PrunedStacks = pruned
		resp.Warnings = append(resp.Warnings, warnings...)
	}
	return httpx.Success(e, http.StatusOK, resp)
}

func demoResetLeaseInventory(
	ctx context.Context,
	leases stackLifecycleLeaseService,
	tenantID string,
) (map[vmlease.LeaseID]vmleases.LeaseInventoryRecord, error) {
	reader, ok := leases.(demoResetLeaseInventoryReader)
	if !ok {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	records, err := reader.ListInventoryByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	byID := make(map[vmlease.LeaseID]vmleases.LeaseInventoryRecord, len(records))
	for _, record := range records {
		byID[record.Lease.ID] = record
	}
	return byID, nil
}

func demoResetCustodyResolutionFor(lease vmlease.Lease, reason string) demoResetCustodyResolution {
	leaseID := string(lease.ID)
	return demoResetCustodyResolution{
		LeaseID:  leaseID,
		Action:   "resolve_custody",
		Endpoint: "/api/v1/monthly-runtimes/" + url.PathEscape(leaseID) + "/resolve-custody",
		Reason:   reason,
	}
}

func demoResetCustodyReviewFor(lease vmlease.Lease, reason string) demoResetCustodyResolution {
	return demoResetCustodyResolution{LeaseID: string(lease.ID), Reason: reason}
}

// authorizeDemoReset validates configuration and the shared secret; it returns
// the demo tenant id on success and a terminal HTTP response error otherwise.
func (h demoResetHandlers) authorizeDemoReset(e *httpx.Event) (string, error) {
	tenantID, err := authorizeDemoAutomation(e, "reset")
	if err != nil || tenantID == "" {
		return "", err
	}
	durable, ready := h.reconciler.(monthlyruntime.DurableReconciliationEnqueuer)
	if h.leases == nil || !ready || durable == nil || !durable.DurableReconciliationReady() {
		return "", httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Demo reset requires the lease authority and durable provider reconciliation", nil)
	}
	return tenantID, nil
}

func demoResetDryRunRequested(e *httpx.Event) bool {
	raw := strings.TrimSpace(e.Request.URL.Query().Get("dry_run"))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}

// selectDemoResetVictims filters the tenant's leases down to the reapable set:
// active monthly-runtime leases that are neither explicitly protected nor
// inside the grace window. It rotates a bounded batch across stable lease-id
// order so failed admissions at the head of the list cannot starve the tail.
func selectDemoResetVictims(leases []vmlease.Lease, resp *demoResetResponse) []vmlease.Lease {
	return selectDemoResetVictimsAt(leases, resp, time.Now().UTC(), demoResetBatchSize())
}

func selectDemoResetVictimsAt(
	leases []vmlease.Lease,
	resp *demoResetResponse,
	now time.Time,
	batchSize int,
) []vmlease.Lease {
	grace := demoResetGraceWindow()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	batchSize = clampDemoResetBatchSize(batchSize)
	eligible := make([]vmlease.Lease, 0, len(leases))
	for _, lease := range leases {
		if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) || !monthlyruntime.LeaseActive(lease) {
			continue
		}
		if demoguard.IsProtectedLease(string(lease.ID)) {
			resp.SkippedProtected++
			continue
		}
		if age := now.Sub(demoResetLeaseCreatedAt(lease, now)); age < grace {
			resp.SkippedGrace++
			continue
		}
		eligible = append(eligible, lease)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return string(eligible[i].ID) < string(eligible[j].ID)
	})
	if len(eligible) <= batchSize {
		return eligible
	}
	start := demoResetRotationStart(len(eligible), batchSize, now)
	victims := make([]vmlease.Lease, batchSize)
	for i := range victims {
		victims[i] = eligible[(start+i)%len(eligible)]
	}
	resp.SkippedCap += len(eligible) - len(victims)
	return victims
}

func demoResetBatchSize() int {
	raw := strings.TrimSpace(os.Getenv(envDemoResetBatchSize))
	if raw == "" {
		return defaultDemoResetBatchSize
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return defaultDemoResetBatchSize
	}
	return clampDemoResetBatchSize(parsed)
}

func clampDemoResetBatchSize(value int) int {
	if value <= 0 {
		return defaultDemoResetBatchSize
	}
	if value > maxDemoResetBatchSize {
		return maxDemoResetBatchSize
	}
	return value
}

func demoResetRotationStart(total, batchSize int, now time.Time) int {
	if total <= batchSize || total == 0 {
		return 0
	}
	// Advance by a stride coprime to the current population. This guarantees
	// that a stable population eventually visits every possible starting point,
	// even when the batch size and population share a factor.
	stride := batchSize
	for stride < total && demoResetGCD(stride, total) != 1 {
		stride++
	}
	periodSeconds := int64(demoResetRotationPeriod / time.Second)
	rotation := now.UTC().Unix() / periodSeconds
	start := (rotation * int64(stride)) % int64(total)
	if start < 0 {
		start += int64(total)
	}
	return int(start)
}

func demoResetGCD(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// admitDemoLeaseCleanup hands the exact lease generation to the same durable
// native provider-control application used by product-driven decommission.
// The reset endpoint never cancels a lease or claims provider absence itself.
func (h demoResetHandlers) admitDemoLeaseCleanup(
	ctx context.Context,
	tenantID string,
	lease vmlease.Lease,
) error {
	stackID := strings.TrimSpace(lease.Metadata["stack_id"])
	ownerID := strings.TrimSpace(lease.Subject.ID)
	if stackID == "" || ownerID == "" {
		return vmleases.ErrLeaseIdentityConflict
	}
	digest, err := vmleases.ResourceGenerationDigest(tenantID, lease)
	if err != nil {
		return err
	}
	return h.reconciler.EnqueueProviderReconciliation(ctx, monthlyruntime.ReconciliationRequest{
		StackID: stackID, TenantID: tenantID, OwnerID: ownerID,
		LeaseID: string(lease.ID), ResourceGenerationDigest: digest,
		Reason: "demo_reset_expired_lease",
	})
}

// pruneDemoOrphanStacks removes demo-tenant stacks that have no live lease and
// no worker. It is deliberately owner-agnostic (everything in the demo tenant
// was created through the shared demo account) but keeps the same fail-closed
// posture as pruneOrphanStacks: unknown attachment ⇒ nothing is deleted.
func (h demoResetHandlers) pruneDemoOrphanStacks(ctx context.Context, tenantID string) (int, []string) {
	warnings := []string{}
	if h.stacks.Stacks == nil {
		return 0, warnings
	}
	stacks, err := h.stacks.Stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return 0, append(warnings, "stack list failed: "+err.Error())
	}
	leases, err := h.leases.ListByTenant(ctx, tenantID)
	if err != nil {
		return 0, append(warnings, "lease list failed (fail-closed, nothing pruned): "+err.Error())
	}
	liveLeaseStacks := demoLiveLeaseStackIDs(leases)
	workerStacks := map[string]bool{}
	if h.stacks.Workers != nil {
		workers, werr := h.stacks.Workers.ListWorkersByTenant(ctx, tenantID)
		if werr != nil {
			return 0, append(warnings, "worker list failed (fail-closed, nothing pruned): "+werr.Error())
		}
		workerStacks = demoWorkerStackIDs(workers)
	}
	pruned := 0
	for _, s := range stacks {
		if liveLeaseStacks[s.ID] || workerStacks[s.ID] {
			continue
		}
		if h.stacks.Registry == nil {
			warnings = append(warnings, "stack "+s.ID+" registry audit unavailable (fail-closed)")
			continue
		}
		servers, serversErr := h.stacks.Registry.ListServerRuntimesByTenant(ctx, tenantID, s.ID)
		if serversErr != nil {
			warnings = append(warnings, "stack "+s.ID+" registry audit failed (fail-closed): "+serversErr.Error())
			continue
		}
		if demoStackHasNonTerminalServer(servers) {
			continue
		}
		if h.stacks.Jobs == nil {
			warnings = append(warnings, "stack "+s.ID+" job audit unavailable (fail-closed)")
			continue
		}
		jobs, jobsErr := h.stacks.Jobs.ListJobsByStack(ctx, tenantID, s.ID, 100)
		if jobsErr != nil {
			warnings = append(warnings, "stack "+s.ID+" job audit failed (fail-closed): "+jobsErr.Error())
			continue
		}
		if demoStackHasUnsafeCleanupJob(jobs) {
			continue
		}
		if err := h.stacks.Stacks.SoftDeleteStack(ctx, tenantID, s.ID); err != nil {
			warnings = append(warnings, "stack "+s.ID+" prune warning: "+err.Error())
			continue
		}
		pruned++
	}
	return pruned, warnings
}

func demoStackHasNonTerminalServer(items []controlplane.ServerRuntime) bool {
	for _, item := range items {
		if strings.ToLower(strings.TrimSpace(item.LifecycleState)) !=
			string(serverregistry.LifecycleDecommissioned) {
			return true
		}
	}
	return false
}

func demoStackHasUnsafeCleanupJob(items []controlplane.Job) bool {
	for _, item := range items {
		state := strings.ToLower(strings.TrimSpace(item.State))
		switch state {
		case "pending", "running", "waiting":
			return true
		}
		jobType := strings.ToLower(strings.TrimSpace(item.Type))
		if (jobType == "destroy" || jobType == "reconcile_lease") && state != "completed" {
			return true
		}
	}
	return false
}

func demoLiveLeaseStackIDs(leases []vmlease.Lease) map[string]bool {
	out := map[string]bool{}
	for _, lease := range leases {
		if lease.CancelledAt != nil {
			continue
		}
		if stackID := strings.TrimSpace(lease.Metadata["stack_id"]); stackID != "" {
			out[stackID] = true
		}
	}
	return out
}

func demoWorkerStackIDs(workers []controlplane.Worker) map[string]bool {
	out := map[string]bool{}
	for _, worker := range workers {
		if stackID := strings.TrimSpace(worker.StackID); stackID != "" {
			out[stackID] = true
		}
	}
	return out
}

func demoResetGraceWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envDemoResetGraceMinutes))
	if raw == "" {
		return defaultDemoResetGraceMinutes * time.Minute
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes < 0 {
		return defaultDemoResetGraceMinutes * time.Minute
	}
	return time.Duration(minutes) * time.Minute
}

// demoResetLeaseCreatedAt derives the lease's age anchor: enrollment start
// stamp when present, else ValidFrom, else RenewedAt; a zero anchor is treated
// as old (reapable) rather than perpetually in grace.
func demoResetLeaseCreatedAt(lease vmlease.Lease, fallback time.Time) time.Time {
	if raw := strings.TrimSpace(lease.Metadata["enrollment_started_at"]); raw != "" {
		if ts, err := time.Parse(time.RFC3339, raw); err == nil {
			return ts
		}
	}
	if !lease.ValidFrom.IsZero() {
		return lease.ValidFrom
	}
	if !lease.RenewedAt.IsZero() {
		return lease.RenewedAt
	}
	return fallback.Add(-24 * time.Hour)
}
