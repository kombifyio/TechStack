// Package routes provides custom HTTP routes for kombifyTechstack API.
//
//nolint:goconst
package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// WorkerMetricWriter accepts worker-originated runtime samples.
type WorkerMetricWriter interface {
	Write([]monitoring.MetricSample) error
}

type WorkerRuntimeLogWriter interface {
	AppendRuntimeLog(grpcserver.AgentLogEntry) grpcserver.AgentLogEntry
}

type WorkerRouteConfig struct {
	MetricWriter         WorkerMetricWriter
	ManagedRuntimeLeases managedRuntimeLeaseLister
	Store                controlplane.WorkerStore
	Servers              controlplane.ServerRuntimeStore
	Registry             controlplane.RegistryStore
	RIL                  controlplane.RILStore
	AgentIdentityIssuer  WorkerAgentIdentityIssuer
	TypedControl         WorkerTypedControl
	StackKitOperations   WorkerStackKitOperations
	RuntimeLogs          WorkerRuntimeLogWriter
}

// WorkerAgentIdentityIssuer is the fail-closed bridge from one claimed
// pairing identity to the mTLS authority used by the agent command channel.
type WorkerAgentIdentityIssuer interface {
	IssueAgentIdentity(context.Context, grpcserver.IssueRequest) (*grpcserver.IssuedIdentity, error)
}

func RegisterWorkerRoutesWithStore(r *httpx.Router, cfg WorkerRouteConfig) {
	if cfg.Store == nil {
		panic("RegisterWorkerRoutesWithStore: worker store required — PocketBase fallback removed (PB retirement)")
	}
	if cfg.Servers == nil {
		panic("RegisterWorkerRoutesWithStore: canonical server store required")
	}
	if _, ok := cfg.Servers.(controlplane.GuardInventoryProjectionStore); !ok {
		panic("RegisterWorkerRoutesWithStore: server store must support atomic Guard inventory projection")
	}
	if _, ok := cfg.Servers.(controlplane.ServerEnrollmentStore); !ok {
		panic("RegisterWorkerRoutesWithStore: server store must support atomic control-plane enrollment")
	}
	if _, ok := cfg.Servers.(controlplane.GuardInventorySatelliteStore); !ok {
		panic("RegisterWorkerRoutesWithStore: server store must support fenced Guard inventory satellites")
	}
	credentialStore, ok := cfg.Store.(controlplane.WorkerCredentialStore)
	if !ok {
		panic("RegisterWorkerRoutesWithStore: worker store must support atomic credential generations")
	}
	enrollmentStore, ok := cfg.Store.(controlplane.WorkerEnrollmentStore)
	if !ok {
		panic("RegisterWorkerRoutesWithStore: worker store must support atomic enrollment claims")
	}
	h := workerRouteHandlers{
		managedRuntimeLeases: cfg.ManagedRuntimeLeases,
		metricWriter:         cfg.MetricWriter,
		wst:                  cfg.Store,
		serverStore:          cfg.Servers,
		registryStore:        cfg.Registry,
		rilStore:             cfg.RIL,
		agentIdentityIssuer:  cfg.AgentIdentityIssuer,
		typedControl:         cfg.TypedControl,
		stackKitOperations:   cfg.StackKitOperations,
		runtimeLogs:          cfg.RuntimeLogs,
		credentialStore:      credentialStore,
		enrollmentStore:      enrollmentStore,
		credentialSecret:     workerauth.SecretFromEnv(),
	}
	r.GET("/api/v1/workers", h.list)
	r.POST("/api/v1/workers/register", h.register)
	r.POST("/v1/ril/servers/connect", h.connectServer)
	r.POST("/v1/ril/servers/{id}/credential/rotate", h.rotateServerCredential)
	r.POST("/api/v1/workers/{id}/approve", h.approve)
	r.POST("/api/v1/workers/{id}/heartbeat", h.heartbeat)
	r.POST("/api/v1/workers/{id}/inventory", h.inventory)
	r.POST("/api/v1/workers/{id}/commands/next", h.nextTypedCommand)
	r.POST("/api/v1/workers/{id}/commands/result", h.submitTypedCommandResult)
	r.POST("/api/v1/workers/{id}/stackkit/operations", h.executeStackKitOperations)
	r.POST("/api/v1/workers/{id}/runtime/logs", h.ingestRuntimeLog)
	r.POST("/api/v1/workers/bootstrap/logs", h.ingestBootstrapLog)
	h.binaryDownloadGuard = newAgentBinaryDownloadGuard()
	r.POST("/api/v1/agent/binary/{os}/{arch}", h.agentBinary)
	r.POST("/api/v1/agent/stackkit-release/{os}/{arch}", h.agentStackKitRelease)
	r.GET("/install.sh", h.installScript)
	r.GET("/install.ps1", h.installScriptPS)
}

type workerRouteHandlers struct {
	managedRuntimeLeases    managedRuntimeLeaseLister
	metricWriter            WorkerMetricWriter
	wst                     controlplane.WorkerStore
	serverStore             controlplane.ServerRuntimeStore
	registryStore           controlplane.RegistryStore
	rilStore                controlplane.RILStore
	agentIdentityIssuer     WorkerAgentIdentityIssuer
	typedControl            WorkerTypedControl
	stackKitOperations      WorkerStackKitOperations
	runtimeLogs             WorkerRuntimeLogWriter
	credentialStore         controlplane.WorkerCredentialStore
	enrollmentStore         controlplane.WorkerEnrollmentStore
	credentialSecret        []byte
	binaryArtifact          func() (agentBinaryArtifact, error)
	stackKitReleaseArtifact func() (agentBinaryArtifact, error)
	binaryDownloadGuard     *agentBinaryDownloadGuard
}

func (h workerRouteHandlers) list(e *httpx.Event) error {
	ownerID, err := requireWorkerOwner(e)
	if err != nil || ownerID == "" {
		return err
	}
	return h.listFromStore(e, ownerID)
}

func (h workerRouteHandlers) listFromStore(e *httpx.Event, ownerID string) error {
	pagination := ksapi.ParsePagination(e.Request)
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.workers.list")
	if tenantErr != nil {
		return tenantErr
	}
	workers, err := h.wst.ListWorkersByTenant(e.Request.Context(), tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch workers", nil)
	}
	tenantManagedRuntimeRecords, err := listTenantManagedRuntimeLeasesChecked(e.Request.Context(), h.managedRuntimeLeases, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch managed runtime inventory", nil)
	}
	managedRuntimeRecords := visibleManagedRuntimeLeases(tenantManagedRuntimeRecords, tenantID, ownerID)
	managedRuntimeLeases := managedRuntimeLeasesFromInventory(managedRuntimeRecords)
	tenantAttachmentAuthorities := nativeActiveManagedRuntimeLeases(tenantManagedRuntimeRecords)
	visibleAttachmentAuthorities := nativeActiveManagedRuntimeLeases(managedRuntimeRecords)
	managedRuntimeRecordByID := make(map[string]vmleases.LeaseInventoryRecord, len(managedRuntimeRecords))
	for index, lease := range managedRuntimeLeases {
		managedRuntimeRecordByID[string(lease.ID)] = managedRuntimeRecords[index]
	}
	_, _, duplicateAuthorityLeaseIDs := managedRuntimeLeaseAuthorityIDs(tenantManagedRuntimeRecords)
	if len(duplicateAuthorityLeaseIDs) > 0 {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Managed runtime authority returned duplicate leases", nil)
	}
	activeManagedLeaseIDs, inactiveManagedLeaseIDs, _ := managedRuntimeLeaseAuthorityIDs(managedRuntimeRecords)
	managedRuntimeLeaseGenerationDigests := managedRuntimeLeaseGenerationDigestProofs(tenantID, managedRuntimeLeases)
	attachmentConflictLeaseIDs := managedRuntimeAttachmentConflictLeaseIDs(workers, tenantAttachmentAuthorities, visibleAttachmentAuthorities, ownerID)
	response := make([]map[string]any, 0, len(workers))
	persistedManagedLeases := make(map[string]int)
	persistedManagedWorkers := make(map[string]controlplane.Worker)
	persistedManagedLeaseCounts := make(map[string]int)
	for _, worker := range workers {
		if worker.OwnerSubjectID == ownerID {
			leaseID := managedLeaseIDForWorker(worker)
			row := workerStoreResponse(worker)
			annotateManagedRuntimeWorkerAuthority(row, leaseID, managedRuntimeRecordByID)
			if leaseID != "" {
				persistedManagedLeaseCounts[leaseID]++
			}
			if existingIndex, exists := persistedManagedLeases[leaseID]; leaseID != "" && exists {
				if preferManagedLeaseWorker(worker, persistedManagedWorkers[leaseID]) {
					response[existingIndex] = row
					persistedManagedWorkers[leaseID] = worker
				}
				continue
			}
			if leaseID != "" {
				persistedManagedLeases[leaseID] = len(response)
				persistedManagedWorkers[leaseID] = worker
			}
			response = append(response, row)
		}
	}
	managedRuntimeItems := projectManagedRuntimeLeaseSnapshot(managedRuntimeRecords, "")
	for _, item := range managedRuntimeItems {
		if _, exists := persistedManagedLeases[item.LeaseID]; exists {
			continue
		}
		response = append(response, managedRuntimeWorkerResponse(item))
	}
	// Pagination is consumed as release evidence. Sort by the globally unique
	// worker id before slicing so page boundaries remain deterministic across
	// Postgres, MemoryStore, and appended managed-runtime projections.
	sort.Slice(response, func(i, j int) bool {
		return stringFromAny(response[i]["id"]) < stringFromAny(response[j]["id"])
	})
	duplicateManagedLeaseIDSet := make(map[string]struct{})
	for leaseID, count := range persistedManagedLeaseCounts {
		if count > 1 {
			duplicateManagedLeaseIDSet[leaseID] = struct{}{}
		}
	}
	duplicateManagedLeaseIDs := make([]string, 0, len(duplicateManagedLeaseIDSet))
	for leaseID := range duplicateManagedLeaseIDSet {
		duplicateManagedLeaseIDs = append(duplicateManagedLeaseIDs, leaseID)
	}
	sort.Strings(duplicateManagedLeaseIDs)
	workerInventorySHA256, err := workerInventoryBindingSHA256(
		response,
		activeManagedLeaseIDs,
		inactiveManagedLeaseIDs,
		duplicateManagedLeaseIDs,
		attachmentConflictLeaseIDs,
		managedRuntimeLeaseGenerationDigests,
	)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to attest worker inventory", nil)
	}
	total := len(response)
	start := pagination.Offset
	if start > len(response) {
		start = len(response)
	}
	end := start + pagination.PerPage
	if end > len(response) {
		end = len(response)
	}
	meta := ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage)
	managedRuntimeInventoryComplete := h.managedRuntimeLeases != nil
	meta.ManagedRuntimeInventoryComplete = &managedRuntimeInventoryComplete
	meta.WorkerInventorySHA256 = workerInventorySHA256
	meta.ManagedRuntimeActiveLeaseIDs = &activeManagedLeaseIDs
	meta.ManagedRuntimeInactiveLeaseIDs = &inactiveManagedLeaseIDs
	meta.ManagedRuntimeLeaseGenerationDigests = &managedRuntimeLeaseGenerationDigests
	meta.ManagedRuntimeDuplicateLeaseIDs = &duplicateManagedLeaseIDs
	meta.ManagedRuntimeAttachmentConflictLeaseIDs = &attachmentConflictLeaseIDs
	return httpx.SuccessWithMeta(e, http.StatusOK, response[start:end], meta)
}

func managedRuntimeLeaseAuthorityIDs(records []vmleases.LeaseInventoryRecord) (active, inactive, duplicate []string) {
	active = make([]string, 0)
	inactive = make([]string, 0)
	duplicate = make([]string, 0)
	counts := make(map[string]int, len(records))
	activeByID := make(map[string]bool, len(records))
	for _, record := range records {
		lease := record.Lease
		leaseID := string(lease.ID)
		counts[leaseID]++
		if record.NativeActive() {
			activeByID[leaseID] = true
		} else if _, exists := activeByID[leaseID]; !exists {
			activeByID[leaseID] = false
		}
	}
	for leaseID, count := range counts {
		if activeByID[leaseID] {
			active = append(active, leaseID)
		} else {
			inactive = append(inactive, leaseID)
		}
		if count > 1 {
			duplicate = append(duplicate, leaseID)
		}
	}
	sort.Strings(active)
	sort.Strings(inactive)
	sort.Strings(duplicate)
	return active, inactive, duplicate
}

func managedRuntimeLeasesFromInventory(records []vmleases.LeaseInventoryRecord) []vmlease.Lease {
	leases := make([]vmlease.Lease, 0, len(records))
	for _, record := range records {
		leases = append(leases, record.Lease)
	}
	return leases
}

func nativeActiveManagedRuntimeLeases(records []vmleases.LeaseInventoryRecord) []vmlease.Lease {
	leases := make([]vmlease.Lease, 0, len(records))
	for _, record := range records {
		if record.NativeActive() {
			leases = append(leases, record.Lease)
		}
	}
	return leases
}

func managedRuntimeLeaseGenerationDigestProofs(tenantID string, leases []vmlease.Lease) []ksapi.ManagedRuntimeLeaseGenerationDigest {
	proofs := make([]ksapi.ManagedRuntimeLeaseGenerationDigest, 0, len(leases))
	for _, lease := range leases {
		digest, err := vmleases.ResourceGenerationDigest(tenantID, lease)
		if err != nil {
			// Keep the ordinary inventory readable for legacy or not-yet-enrolled
			// leases. Destructive proof consumers require exact 1:1 coverage and
			// therefore fail closed when any visible lease is omitted here.
			continue
		}
		proofs = append(proofs, ksapi.ManagedRuntimeLeaseGenerationDigest{
			LeaseID:                  strings.TrimSpace(string(lease.ID)),
			ResourceGenerationDigest: digest,
		})
	}
	sort.Slice(proofs, func(i, j int) bool {
		return proofs[i].LeaseID < proofs[j].LeaseID
	})
	return proofs
}

func annotateManagedRuntimeWorkerAuthority(row map[string]any, leaseID string, records map[string]vmleases.LeaseInventoryRecord) {
	if leaseID == "" {
		return
	}
	row["runtime_lane"] = serverruntime.RuntimeLaneMonthly
	record, ok := records[leaseID]
	if !ok {
		row["managed_runtime_lease_authority_state"] = "missing"
		row["approved"] = false
		row["approved_at"] = nil
		row["assignable"] = false
		return
	}
	lease := record.Lease
	row["desired_state"] = string(lease.DesiredState)
	row["execution_authority"] = string(record.ExecutionAuthority)
	row["managed_runtime_lease_authority_state"] = string(record.AuthorityState)
	if !record.NativeActive() {
		row["approved"] = false
		row["approved_at"] = nil
		row["assignable"] = false
		row["status"] = managedRuntimeAuthorityStatus(record)
	}
}

// managedRuntimeAttachmentConflictLeaseIDs attests tenant-wide attachments
// against caller-visible lease identities. Hidden leases and workers may mark
// a visible lease as conflicted when they share its stack, but their own ids or
// details never cross the response boundary.
func managedRuntimeAttachmentConflictLeaseIDs(workers []controlplane.Worker, tenantLeases, visibleLeases []vmlease.Lease, ownerID string) []string {
	visibility := newManagedRuntimeAttachmentVisibility(visibleLeases)
	visibility.markHiddenTenantLeaseConflicts(tenantLeases)
	for _, worker := range workers {
		visibility.markWorkerConflict(worker, ownerID)
	}
	return visibility.sortedConflictLeaseIDs()
}

type managedRuntimeAttachmentVisibility struct {
	visibleLeaseByID         map[string]vmlease.Lease
	visibleLeaseIDByServerID map[string]string
	visibleLeaseIDsByStackID map[string][]string
	conflicts                map[string]struct{}
}

func newManagedRuntimeAttachmentVisibility(visibleLeases []vmlease.Lease) *managedRuntimeAttachmentVisibility {
	visibility := &managedRuntimeAttachmentVisibility{
		visibleLeaseByID:         make(map[string]vmlease.Lease, len(visibleLeases)),
		visibleLeaseIDByServerID: make(map[string]string, len(visibleLeases)),
		visibleLeaseIDsByStackID: make(map[string][]string),
		conflicts:                make(map[string]struct{}),
	}
	for _, lease := range visibleLeases {
		leaseID := strings.TrimSpace(string(lease.ID))
		if leaseID == "" {
			continue
		}
		visibility.visibleLeaseByID[leaseID] = lease
		visibility.visibleLeaseIDByServerID[runtimeidentity.LeaseServerID(leaseID)] = leaseID
		if stackID := strings.TrimSpace(lease.Metadata["stack_id"]); stackID != "" {
			visibility.visibleLeaseIDsByStackID[stackID] = append(visibility.visibleLeaseIDsByStackID[stackID], leaseID)
		}
	}

	for stackID := range visibility.visibleLeaseIDsByStackID {
		sort.Strings(visibility.visibleLeaseIDsByStackID[stackID])
	}
	return visibility
}

func (v *managedRuntimeAttachmentVisibility) markConflict(leaseID string) {
	if _, visible := v.visibleLeaseByID[leaseID]; visible {
		v.conflicts[leaseID] = struct{}{}
	}
}

func (v *managedRuntimeAttachmentVisibility) markStackConflicts(stackID string) {
	for _, leaseID := range v.visibleLeaseIDsByStackID[strings.TrimSpace(stackID)] {
		v.markConflict(leaseID)
	}
}

func (v *managedRuntimeAttachmentVisibility) markHiddenTenantLeaseConflicts(tenantLeases []vmlease.Lease) {
	// A caller must not be allowed to prove a visible stack unattached while a
	// private active lease in the same tenant still protects it. Bind that
	// hidden fact only to caller-visible ids for the shared stack.
	for _, lease := range tenantLeases {
		leaseID := strings.TrimSpace(string(lease.ID))
		if _, visible := v.visibleLeaseByID[leaseID]; visible || !managedRuntimeLeaseActive(lease) {
			continue
		}
		v.markStackConflicts(lease.Metadata["stack_id"])
	}
}

func (v *managedRuntimeAttachmentVisibility) markWorkerConflict(worker controlplane.Worker, ownerID string) {
	ownerID = strings.TrimSpace(ownerID)
	rawLeaseID := strings.TrimSpace(runtimeLeaseIDFromMetadata(worker.Capabilities))
	rawServerID := stringFromAny(worker.Capabilities["server_id"])
	rawStackID := strings.TrimSpace(worker.StackID)
	foreignOrUnowned := strings.TrimSpace(worker.OwnerSubjectID) != ownerID

	lease, rawLeaseVisible := v.visibleLeaseByID[rawLeaseID]
	serverLeaseID, serverMatchesVisibleLease := v.visibleLeaseIDByServerID[rawServerID]

	if rawLeaseVisible {
		expectedServerID := runtimeidentity.LeaseServerID(rawLeaseID)
		authorityStackID := strings.TrimSpace(lease.Metadata["stack_id"])
		if foreignOrUnowned ||
			(rawServerID != "" && rawServerID != expectedServerID) ||
			(authorityStackID != "" && rawStackID != "" && rawStackID != authorityStackID) {
			v.markConflict(rawLeaseID)
		}
	}

	if serverMatchesVisibleLease && (foreignOrUnowned || !rawLeaseVisible || rawLeaseID != serverLeaseID) {
		v.markConflict(serverLeaseID)
	}
	// Stack-only evidence cannot select one lease when several visible leases
	// share the stack. Mark every candidate instead of silently accepting an
	// ambiguous destructive proof.
	if !rawLeaseVisible && !serverMatchesVisibleLease {
		v.markStackConflicts(rawStackID)
	}
}

func (v *managedRuntimeAttachmentVisibility) sortedConflictLeaseIDs() []string {
	leaseIDs := make([]string, 0, len(v.conflicts))
	for leaseID := range v.conflicts {
		leaseIDs = append(leaseIDs, leaseID)
	}
	sort.Strings(leaseIDs)
	return leaseIDs
}

func workerInventoryBindingSHA256(workers []map[string]any, activeLeaseIDs, inactiveLeaseIDs, duplicateLeaseIDs, attachmentConflictLeaseIDs []string, generationDigests []ksapi.ManagedRuntimeLeaseGenerationDigest) (string, error) {
	bindings := make([]map[string]any, 0, len(workers))
	for _, worker := range workers {
		bindings = append(bindings, map[string]any{
			"id":                                    worker["id"],
			"stack_id":                              worker["stack_id"],
			"lease_id":                              worker["lease_id"],
			"source":                                worker["source"],
			"runtime_lane":                          worker["runtime_lane"],
			"desired_state":                         worker["desired_state"],
			"managed_runtime_lease_authority_state": worker["managed_runtime_lease_authority_state"],
			"status":                                worker["status"],
			"created":                               worker["created"],
			"last_seen":                             worker["last_seen"],
			"decommissioned_at":                     worker["decommissioned_at"],
			"deleted_at":                            worker["deleted_at"],
		})
	}
	snapshot := struct {
		Workers                    []map[string]any                            `json:"workers"`
		ActiveLeaseIDs             []string                                    `json:"active_lease_ids"`
		InactiveLeaseIDs           []string                                    `json:"inactive_lease_ids"`
		DuplicateIDs               []string                                    `json:"duplicate_lease_ids"`
		AttachmentConflictLeaseIDs []string                                    `json:"attachment_conflict_lease_ids"`
		LeaseGenerationDigests     []ksapi.ManagedRuntimeLeaseGenerationDigest `json:"lease_generation_digests"`
	}{
		Workers:                    bindings,
		ActiveLeaseIDs:             activeLeaseIDs,
		InactiveLeaseIDs:           inactiveLeaseIDs,
		DuplicateIDs:               duplicateLeaseIDs,
		AttachmentConflictLeaseIDs: attachmentConflictLeaseIDs,
		LeaseGenerationDigests:     generationDigests,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func workerStoreResponse(worker controlplane.Worker) map[string]any {
	tags := ""
	if raw, ok := worker.Tags["raw"].(string); ok {
		tags = raw
	}
	leaseID := managedLeaseIDForWorker(worker)
	return map[string]any{
		"id":               worker.ID,
		"server_id":        workerServerID(worker, leaseID),
		"lease_id":         leaseID,
		"hostname":         worker.Hostname,
		"ip":               worker.IP,
		"os":               worker.OS,
		"arch":             worker.Arch,
		"stack_id":         worker.StackID,
		"approved":         worker.Approved,
		"approved_at":      worker.ApprovedAt,
		"last_seen":        worker.LastSeenAt,
		"created":          worker.CreatedAt,
		"cpu_cores":        worker.CPUCores,
		"ram_mb":           worker.RAMMB,
		"disk_gb":          worker.DiskGB,
		"gpu":              worker.GPU,
		"has_nvme":         worker.HasNVME,
		"has_hw_transcode": worker.HasHWTranscode,
		"docker_version":   worker.DockerVersion,
		"type":             worker.Type,
		"provider":         worker.Provider,
		"tags":             tags,
		"tenant_id":        worker.TenantID,
		"connected_at":     worker.CreatedAt,
		"status":           worker.Status,
		"source":           workerRegistryInventorySource,
		"assignable":       true,
	}
}

func managedLeaseIDForWorker(worker controlplane.Worker) string {
	leaseID := runtimeLeaseIDFromMetadata(worker.Capabilities)
	if leaseID == "" {
		return ""
	}
	if reportedServerID := stringFromAny(worker.Capabilities["server_id"]); reportedServerID != "" && reportedServerID != runtimeidentity.LeaseServerID(leaseID) {
		return ""
	}
	return leaseID
}

func workerServerID(worker controlplane.Worker, leaseID string) string {
	return firstNonEmpty(
		runtimeidentity.LeaseServerID(leaseID),
		stringFromAny(worker.Capabilities["server_id"]),
		runtimeServerIDForWorker(worker.ID),
	)
}

func preferManagedLeaseWorker(candidate, current controlplane.Worker) bool {
	candidateObservedAt := workerObservedAt(candidate)
	currentObservedAt := workerObservedAt(current)
	if !candidateObservedAt.Equal(currentObservedAt) {
		return candidateObservedAt.After(currentObservedAt)
	}
	return candidate.ID < current.ID
}

func workerObservedAt(worker controlplane.Worker) time.Time {
	if worker.LastSeenAt != nil {
		return worker.LastSeenAt.UTC()
	}
	if !worker.UpdatedAt.IsZero() {
		return worker.UpdatedAt.UTC()
	}
	return worker.CreatedAt.UTC()
}

type workerRegistrationRequest struct {
	Token          string  `json:"token"`
	Hostname       string  `json:"hostname"`
	OS             string  `json:"os"`
	Arch           string  `json:"arch"`
	CPU            float64 `json:"cpu_cores"`
	RAMMB          float64 `json:"ram_mb"`
	DiskGB         float64 `json:"disk_gb"`
	DockerVersion  string  `json:"docker_version"`
	Type           string  `json:"type"`
	Provider       string  `json:"provider"`
	Tags           string  `json:"tags"`
	HasNVME        bool    `json:"has_nvme"`
	HasHWTranscode bool    `json:"has_hw_transcode"`
	GPU            string  `json:"gpu"`
}

const workerRegistrationMaxBodyBytes int64 = 64 << 10

type workerHeartbeatRequest struct {
	SourceEpoch        string                       `json:"source_epoch"`
	SourceSequence     int64                        `json:"source_sequence"`
	ObservedAt         time.Time                    `json:"observed_at"`
	CPUPercent         float64                      `json:"cpu_percent"`
	MemoryUsedBytes    int64                        `json:"memory_used_bytes"`
	MemoryTotalBytes   int64                        `json:"memory_total_bytes"`
	DiskUsedBytes      int64                        `json:"disk_used_bytes"`
	DiskTotalBytes     int64                        `json:"disk_total_bytes"`
	UptimeSeconds      float64                      `json:"uptime_seconds"`
	RuntimeConvergence *runtimeconvergence.Snapshot `json:"runtime_convergence,omitempty"`
}

func (h workerRouteHandlers) register(e *httpx.Event) error {
	req, validationErr, ok := readWorkerRegistrationRequest(e)
	if !ok {
		return httpx.BadRequest(e, validationErr, nil)
	}

	return h.registerWithStore(e, req)
}

func (h workerRouteHandlers) registerWithStore(e *httpx.Event, req workerRegistrationRequest) error {
	now := time.Now().UTC()
	token, tokenHash, tokErr := h.claimStorePairingToken(e, req.Token, now)
	if tokErr != nil || token == nil {
		return tokErr
	}

	leaseID := runtimeLeaseIDFromMetadata(token.Metadata)
	workerID := workerStoreID(token.TenantID, tokenHash, req.Hostname)
	if leaseID != "" {
		workerID = workerStoreIDForLease(token.TenantID, leaseID)
	}
	if ownerErr := h.ensureStoreWorkerOwnership(e, *token, workerID); ownerErr != nil {
		return ownerErr
	}
	serverID := firstNonEmpty(runtimeidentity.LeaseServerID(leaseID), runtimeServerIDForWorker(workerID))
	if leaseID == "" {
		serverID = firstNonEmpty(
			h.plannedServerIDForStack(e.Request.Context(), token.TenantID, token.OwnerSubjectID, token.StackID),
			serverID,
		)
	}
	agentToken, agentTokenErr := h.issueRuntimeAgentToken()
	if agentTokenErr != nil || strings.TrimSpace(agentToken) == "" {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create runtime agent credential", nil)
	}

	worker, err := h.wst.UpsertWorkerHeartbeat(e.Request.Context(), buildStoreWorker(*token, req, storeWorkerContext{
		workerID:  workerID,
		serverID:  serverID,
		tokenHash: tokenHash,
		agentHash: workerauth.SHA256Hex(agentToken),
		clientIP:  getClientIP(e),
		now:       now,
	}))
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to save worker registration", nil)
	}
	if err := h.projectServerEnrollment(e.Request.Context(), *worker, serverID, leaseID, now, "pairing-redemption"); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to persist server enrollment", nil)
	}
	var issuedIdentity *grpcserver.IssuedIdentity
	if h.agentIdentityIssuer != nil {
		issuedIdentity, err = h.agentIdentityIssuer.IssueAgentIdentity(e.Request.Context(), grpcserver.IssueRequest{
			TenantID: worker.TenantID,
			AgentID:  worker.ID,
			AllowedCommandClasses: []string{
				"health_check",
				"get_logs",
				"stackkit",
			},
			EnrolledBy: "pairing-redemption",
		})
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to issue runtime agent identity", nil)
		}
	}
	accepted := worker.Approved && worker.Status == "approved"
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, h.workerEnrollmentResponse(e, workerEnrollmentContext{
		WorkerID:       worker.ID,
		ServerID:       serverID,
		RuntimeAgentID: worker.ID,
		TenantID:       worker.TenantID,
		OwnerID:        worker.OwnerSubjectID,
		StackID:        worker.StackID,
		LeaseID:        leaseID,
		Accepted:       accepted,
		AgentToken:     agentToken,
		GRPCIdentity:   issuedIdentity,
	}))
}

// resolveStorePairingToken resolves the pairing capability within the tenant
// encoded by current tokens. Retired opaque tokens retain a bounded global
// fallback for legacy stores; FORCE RLS intentionally makes that fallback miss
// in the canonical Postgres runtime, so the UI must issue a fresh current token.
func (h workerRouteHandlers) resolveStorePairingToken(e *httpx.Event, rawToken string) (*controlplane.PairingToken, string, error) {
	parsed, parseErr := pairingtoken.Parse(rawToken)
	if parseErr != nil {
		return nil, "", httpx.Unauthorized(e, "Invalid or expired token")
	}
	token, err := h.wst.GetPairingTokenByHash(e.Request.Context(), parsed.TenantID, parsed.TokenHash)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, "", httpx.Unauthorized(e, "Invalid or expired token")
	}
	if err != nil || token == nil {
		return nil, "", httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to validate pairing token", nil)
	}
	if parsed.TenantID != "" && token.TenantID != parsed.TenantID {
		return nil, "", httpx.Unauthorized(e, "Invalid or expired token")
	}
	if token.Status != "active" || token.UsedAt != nil {
		return nil, "", httpx.Unauthorized(e, "Token already used")
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now().UTC()) {
		return nil, "", httpx.Unauthorized(e, "Token expired")
	}
	if token.OwnerSubjectID == "" {
		return nil, "", httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Token missing user", nil)
	}
	return token, parsed.TokenHash, nil
}

// claimStorePairingToken is the authorization boundary for enrollment. Current
// tokens carry the tenant scope needed for the store's atomic compare-and-swap;
// retired opaque tokens must be replaced through the UI before registration.
func (h workerRouteHandlers) claimStorePairingToken(e *httpx.Event, rawToken string, claimedAt time.Time) (*controlplane.PairingToken, string, error) {
	parsed, parseErr := pairingtoken.Parse(rawToken)
	if parseErr != nil || parsed.Legacy {
		return nil, "", httpx.Unauthorized(e, "Invalid or expired token")
	}
	token, err := h.wst.ClaimPairingToken(e.Request.Context(), parsed.TenantID, parsed.TokenHash, claimedAt)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil, "", httpx.Unauthorized(e, "Invalid or expired token")
	}
	if err != nil || token == nil {
		return nil, "", httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to claim pairing token", nil)
	}
	if token.TenantID != parsed.TenantID || token.TokenHash != parsed.TokenHash || token.Status != "used" || token.UsedAt == nil {
		return nil, "", httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to claim pairing token", nil)
	}
	if token.OwnerSubjectID == "" {
		return nil, "", httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Token missing user", nil)
	}
	return token, parsed.TokenHash, nil
}

// ensureStoreWorkerOwnership rejects re-registration of an existing worker that
// is already claimed by a different owner. A missing worker is not an error.
func (h workerRouteHandlers) ensureStoreWorkerOwnership(e *httpx.Event, token controlplane.PairingToken, workerID string) error {
	existing, err := h.wst.GetWorker(e.Request.Context(), token.TenantID, workerID)
	if err != nil {
		return nil
	}
	if existing.OwnerSubjectID != "" && existing.OwnerSubjectID != token.OwnerSubjectID {
		return httpx.Forbidden(e, "Not allowed")
	}
	return nil
}

// storeWorkerContext carries the derived (non-payload) values needed to build a
// control-plane Worker, keeping buildStoreWorker within the argument budget.
type storeWorkerContext struct {
	workerID  string
	serverID  string
	tokenHash string
	agentHash string
	clientIP  string
	now       time.Time
}

// buildStoreWorker maps a validated pairing token plus registration payload into
// the control-plane Worker upserted on registration.
func buildStoreWorker(token controlplane.PairingToken, req workerRegistrationRequest, ctx storeWorkerContext) controlplane.Worker {
	metadata := workerRegistrationMetadata(token.Metadata, req.Tags)
	workerType := strings.TrimSpace(req.Type)
	if role := nodehandoff.StringFromMap(metadata, nodehandoff.KeyServerNodeRole); role != "" {
		workerType = nodehandoff.WorkerTypeForNodeRole(role)
	}
	tags := nodehandoff.MergeTags(req.Tags, metadata)
	resources := workerRegistrationResources(metadata)
	if ctx.agentHash != "" {
		if resources == nil {
			resources = map[string]any{}
		}
		resources["agent_token_sha256"] = ctx.agentHash
	}
	capabilities := workerRegistrationCapabilities(metadata)
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	capabilities["server_id"] = firstNonEmpty(ctx.serverID, runtimeServerIDForWorker(ctx.workerID))
	capabilities["runtime_agent_id"] = ctx.workerID
	if leaseID := runtimeLeaseIDFromMetadata(metadata); leaseID != "" {
		capabilities[runtimeLeaseIDKey] = leaseID
		capabilities["server_id"] = runtimeidentity.LeaseServerID(leaseID)
	}
	// A stack-scoped pairing token is created by the stack owner
	// (createPairingTokenFromStore requires owning the stack) and is validated
	// active/unexpired/owner-bound before we reach here, so it IS the operator's
	// approval of a worker for that stack. Stamping the worker approved on the
	// spot puts the BYOS lane on par with the managed lane (which deploys off the
	// lease) — no redundant manual /approve click, no ErrNoAssignedWorkers. A
	// token without a stack scope stays pending: it cannot satisfy the
	// stack-assigned deploy filter anyway and must not be blanket-approved.
	status, approved, approvedAt := "pending", false, (*time.Time)(nil)
	if strings.TrimSpace(token.StackID) != "" {
		status, approved, approvedAt = "approved", true, &ctx.now
	}
	return controlplane.Worker{
		ID:             ctx.workerID,
		TenantID:       token.TenantID,
		InstanceID:     token.InstanceID,
		StackID:        token.StackID,
		Hostname:       req.Hostname,
		IP:             ctx.clientIP,
		OS:             req.OS,
		Arch:           req.Arch,
		TokenHash:      ctx.tokenHash,
		Status:         status,
		Approved:       approved,
		ApprovedAt:     approvedAt,
		LastSeenAt:     &ctx.now,
		CPUCores:       int(req.CPU),
		RAMMB:          int(req.RAMMB),
		DiskGB:         int(req.DiskGB),
		GPU:            req.GPU,
		HasNVME:        req.HasNVME,
		HasHWTranscode: req.HasHWTranscode,
		DockerVersion:  req.DockerVersion,
		Type:           workerType,
		Provider:       req.Provider,
		Tags:           map[string]any{"raw": tags},
		OwnerSubjectID: token.OwnerSubjectID,
		Capabilities:   capabilities,
		Resources:      resources,
	}
}

func workerRegistrationMetadata(tokenMetadata map[string]any, rawTags string) map[string]any {
	metadata := nodehandoff.MergeMetadata(tokenMetadata, nodehandoff.MetadataFromTags(rawTags))
	if len(metadata) == 0 {
		return nil
	}
	if role := nodehandoff.StringFromMap(metadata, nodehandoff.KeyServerNodeRole); role != "" {
		metadata[nodehandoff.KeyServerNodeRole] = nodehandoff.NormalizeNodeRole(role)
	}
	if services := nodehandoff.ServiceKeysFromAny(metadata[nodehandoff.KeyRequestedServices]); len(services) > 0 {
		metadata[nodehandoff.KeyRequestedServices] = services
	}
	return metadata
}

func workerRegistrationCapabilities(metadata map[string]any) map[string]any {
	out := map[string]any{}
	if role := nodehandoff.StringFromMap(metadata, nodehandoff.KeyServerNodeRole); role != "" {
		out[nodehandoff.KeyServerNodeRole] = nodehandoff.NormalizeNodeRole(role)
		out["node_role"] = nodehandoff.NormalizeNodeRole(role)
	}
	if services := nodehandoff.ServiceKeysFromAny(metadata[nodehandoff.KeyRequestedServices]); len(services) > 0 {
		out[nodehandoff.KeyRequestedServices] = services
		out["services"] = services
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func workerRegistrationResources(metadata map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		nodehandoff.KeyServerRemoteHost,
		nodehandoff.KeyServerRemoteUser,
		nodehandoff.KeyServerRemoteAuthMethod,
		nodehandoff.KeyServerRemoteCredential,
		nodehandoff.KeyServerRemoteSSHKey,
	} {
		if value := nodehandoff.StringFromMap(metadata, key); value != "" {
			out[key] = value
		}
	}
	if port := nodehandoff.IntFromMap(metadata, nodehandoff.KeyServerRemotePort); port > 0 {
		out[nodehandoff.KeyServerRemotePort] = port
	}
	if nodehandoff.BoolFromMap(metadata, nodehandoff.KeyServerRemoteUseSudo) {
		out[nodehandoff.KeyServerRemoteUseSudo] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func workerStoreID(tenantID, tokenHash, hostname string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tenantID),
		strings.TrimSpace(tokenHash),
		strings.ToLower(strings.TrimSpace(hostname)),
	}, "\x00")))
	return "worker_" + hex.EncodeToString(sum[:])[:24]
}

func workerStoreIDForLease(tenantID, leaseID string) string {
	return runtimeidentity.LeaseRuntimeAgentID(tenantID, leaseID)
}

func readWorkerRegistrationRequest(e *httpx.Event) (workerRegistrationRequest, string, bool) {
	var req workerRegistrationRequest
	body, err := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, workerRegistrationMaxBodyBytes))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return req, "Request body too large", false
		}
		return req, "Invalid request body", false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return req, "Invalid request body", false
	}
	req.Token = strings.TrimSpace(req.Token)
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.OS = strings.TrimSpace(req.OS)
	req.Arch = strings.TrimSpace(req.Arch)
	req.DockerVersion = strings.TrimSpace(req.DockerVersion)
	req.Type = strings.TrimSpace(req.Type)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Tags = strings.TrimSpace(req.Tags)
	req.GPU = strings.TrimSpace(req.GPU)
	if req.Token == "" {
		return req, "token is required", false
	}
	if req.Hostname == "" {
		return req, "hostname is required", false
	}
	return req, "", true
}

// approve approves a worker through the control-plane store.
func (h workerRouteHandlers) approve(e *httpx.Event) error {
	ownerID, authErr := requireWorkerOwner(e)
	if authErr != nil || ownerID == "" {
		return authErr
	}
	id := e.Request.PathValue("id")
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.workers.approve")
	if tenantErr != nil {
		return tenantErr
	}
	_, err := h.wst.ApproveWorker(e.Request.Context(), tenantID, id, ownerID, time.Now().UTC())
	if err != nil {
		if err == controlplane.ErrNotFound {
			return httpx.NotFound(e, "Worker not found")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to approve worker", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{routeMessageField: "Worker approved"})
}

func requireWorkerOwner(e *httpx.Event) (string, error) {
	ownerID, err := requireAuth(e)
	if err != nil || ownerID == "" {
		return "", err
	}
	return ownerID, nil
}
