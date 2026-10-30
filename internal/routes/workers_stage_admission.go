package routes

import (
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/releasechannel"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// WorkerMachineRoute is the finite existing installer/Guard transport surface.
// It conveys no authentication: every dynamic handler still verifies its own
// pairing capability or exact worker credential, host proof and native lease.
func WorkerMachineRoute(method, path string) bool {
	if method == http.MethodGet {
		return path == "/install.sh"
	}
	if method != http.MethodPost {
		return false
	}
	if path == "/api/v1/workers/register" || path == "/api/v1/workers/bootstrap/logs" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "agent" &&
		parts[4] == "linux" && (parts[5] == "amd64" || parts[5] == "arm64" || parts[5] == "arm") {
		return parts[3] == "binary" || parts[3] == "release-evidence" || parts[3] == "stackkit-release"
	}
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "workers" || parts[3] == "" {
		return false
	}
	switch strings.Join(parts[4:], "/") {
	case "heartbeat", "inventory", "commands/next", "commands/result", "provider/next", "provider/result", "provider/bootstrap",
		"stackkit/operations", "runtime/logs", "identity/token", "identity/bind", "identity/rotate", "reach", "posture", "telemetry", "user-notification":
		return true
	default:
		return false
	}
}

// requireStageWorkerLease reuses the canonical native inventory and server
// bindings. A tenant hint, capability locator or a valid host proof alone can
// never admit a different Stage owner or a foreign/unbound lease. This read
// must precede pairing claims, credential writes and proof nonce consumption.
func (h workerRouteHandlers) requireStageWorkerLease(e *httpx.Event, tenant, owner, stack, leaseID, serverID, workerID string) error {
	if !releasechannel.StageRuntimePrepared() {
		return nil
	}
	refuse := tenantguard.ExecutionRefusal(releasechannel.ErrTenantNotOwned)
	if !releasechannel.StageOwnerActive() || !releasechannel.OwnsTenant(tenant) || !releasechannel.OwnsSubject(owner) ||
		stack == "" || leaseID == "" || serverID != runtimeidentity.LeaseServerID(leaseID) || workerID == "" {
		return refuse
	}
	records, err := listTenantManagedRuntimeLeasesChecked(e.Request.Context(), h.managedRuntimeLeases, tenant)
	if err != nil {
		return httpx.NewAPIError(http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Native managed lease authority is unavailable", nil)
	}
	bound := false
	for _, record := range records {
		if string(record.Lease.ID) == leaseID && record.NativeActive() &&
			managedRuntimeLeaseVisibleToOwner(record.Lease, tenant, owner) && record.Lease.Metadata["stack_id"] == stack {
			bound = true
			break
		}
	}
	if !bound || h.serverStore == nil {
		return refuse
	}
	server, err := h.serverStore.GetServerRuntime(e.Request.Context(), tenant, serverID)
	if err != nil || server == nil || server.TenantID != tenant || server.OwnerSubjectID != owner ||
		server.StackID != stack || server.LeaseID != leaseID || (server.WorkerID != "" && server.WorkerID != workerID) {
		return refuse
	}
	return nil
}

func (h workerRouteHandlers) requireStagePairingLease(e *httpx.Event, token *controlplane.PairingToken) error {
	if token == nil || !releasechannel.StageRuntimePrepared() {
		return nil
	}
	leaseID := runtimeLeaseIDFromMetadata(token.Metadata)
	return h.requireStageWorkerLease(e, token.TenantID, token.OwnerSubjectID, token.StackID, leaseID,
		runtimeidentity.LeaseServerID(leaseID), workerStoreIDForLease(token.TenantID, leaseID))
}

func (h workerRouteHandlers) requireStageEnrolledLease(e *httpx.Event, worker *controlplane.Worker) error {
	if worker == nil {
		return tenantguard.ExecutionRefusal(releasechannel.ErrTenantNotOwned)
	}
	return h.requireStageWorkerLease(e, worker.TenantID, worker.OwnerSubjectID, worker.StackID,
		runtimeLeaseIDFromMetadata(worker.Capabilities), stringFromAny(worker.Capabilities[workerFieldServerID]), worker.ID)
}

func writeStageWorkerAdmissionError(e *httpx.Event, err error) {
	if refusal, ok := err.(*httpx.APIError); ok {
		_ = httpx.Error(e, refusal.Status, refusal.Code, refusal.Message, refusal.Details)
		return
	}
	_ = httpx.InternalError(e, "Worker admission is unavailable")
}
