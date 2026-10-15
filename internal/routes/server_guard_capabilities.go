package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

// ServerWorkerReader reads the worker rows that carry the facts a Guard reports
// about itself. controlplane.WorkerStore satisfies it. Nil leaves the
// capabilities section of the server read model absent.
type ServerWorkerReader interface {
	ListWorkersByTenant(ctx context.Context, tenantID string) ([]controlplane.Worker, error)
	GetWorker(ctx context.Context, tenantID, workerID string) (*controlplane.Worker, error)
}

// Identity binding values of the capabilities section.
const (
	serverIdentityKeyBound     = "key_bound"
	serverIdentityLegacyBearer = "legacy_bearer"
	serverIdentityUnknown      = "unknown"
)

// serverGuardOperations is the control-plane view of a Guard's durable
// operation journal, as its heartbeat reported it. Durable is true only while
// the journal is writable and nothing is left unresolved.
type serverGuardOperations struct {
	Contract                 string `json:"contract"`
	State                    string `json:"state,omitempty"`
	Durable                  bool   `json:"durable"`
	UnresolvedOutcomeUnknown int    `json:"unresolved_outcome_unknown"`
	UnackedResults           int    `json:"unacked_results"`
	InFlight                 int    `json:"in_flight"`
	JournalBytes             int64  `json:"journal_bytes,omitempty"`
	JournalBudgetBytes       int64  `json:"journal_budget_bytes,omitempty"`
	Reason                   string `json:"reason,omitempty"`
}

// serverIdentityUpgrade is the structured upgrade guidance of a legacy server.
// Code is the stable identifier the client localizes.
type serverIdentityUpgrade struct {
	Required bool   `json:"required"`
	Code     string `json:"code"`
}

// serverGuardPosture is how a Guard is isolated and how its latest update went.
// A Guard that reports nothing predates the privilege split and reads as
// legacy_root, never as isolated.
type serverGuardPosture struct {
	Mode          string `json:"mode"`
	Reported      bool   `json:"reported"`
	DenyMutations bool   `json:"deny_mutations"`
	UpdateState   string `json:"update_state,omitempty"`
	UpdateVersion string `json:"update_version,omitempty"`
	UpdateReason  string `json:"update_reason,omitempty"`
}

// serverRuntimeCapabilities are the Guard facts of one server, exactly as the
// control plane persisted them. They carry no credential and no key material;
// CredentialGeneration is the integer the fenced credential rotation must echo.
type serverRuntimeCapabilities struct {
	IdentityBinding string `json:"identity_binding"`
	// IdentityUpgrade is present while the server authenticates with its bearer
	// credential alone: it reports and updates itself but receives no mutating
	// command until the owner approves its identity key.
	IdentityUpgrade      *serverIdentityUpgrade `json:"identity_upgrade,omitempty"`
	GuardOperations      *serverGuardOperations `json:"guard_operations,omitempty"`
	GuardPosture         *serverGuardPosture    `json:"guard_posture,omitempty"`
	CredentialGeneration int64                  `json:"credential_generation"`
	stackKitCapabilities []string
	runtimeConvergence   *runtimeconvergence.Snapshot
}

// serverCapabilitiesFromWorker projects the worker row. Facts the worker never
// reported are absent rather than invented.
func serverCapabilitiesFromWorker(worker controlplane.Worker) *serverRuntimeCapabilities {
	out := &serverRuntimeCapabilities{IdentityBinding: serverIdentityUnknown}
	var reported []string
	if roundTrip(worker.Capabilities["stackkit_capabilities"], &reported) {
		out.stackKitCapabilities, _ = normalizeObservedStackKitCapabilities(reported)
	}
	var convergence runtimeconvergence.Snapshot
	if roundTrip(worker.Capabilities["runtime_convergence"], &convergence) {
		if normalized, err := runtimeconvergence.Normalize(convergence); err == nil {
			out.runtimeConvergence = &normalized
		}
	}
	switch strings.TrimSpace(stringFromAny(worker.Capabilities[workerCapabilityIdentityBinding])) {
	case controlplane.IdentityBindingHostKey:
		out.IdentityBinding = serverIdentityKeyBound
	case controlplane.IdentityBindingLegacyBearer:
		out.IdentityBinding = serverIdentityLegacyBearer
		out.IdentityUpgrade = &serverIdentityUpgrade{Required: true, Code: guardidentity.CodeIdentityUpgradeRequired}
	}
	if state, err := controlplane.WorkerCredentialStateFromWorker(worker); err == nil {
		out.CredentialGeneration = state.Generation
	}
	if raw, ok := worker.Capabilities["guard_operations"]; ok {
		var in struct {
			Contract                 string `json:"contract"`
			Journal                  string `json:"journal"`
			Durable                  bool   `json:"durable"`
			UnresolvedOutcomeUnknown int    `json:"unresolved_outcome_unknown"`
			UnackedResults           int    `json:"unacked_results"`
			InFlight                 int    `json:"in_flight"`
			JournalBytes             int64  `json:"journal_bytes"`
			JournalBudgetBytes       int64  `json:"journal_budget_bytes"`
			Reason                   string `json:"reason"`
		}
		if roundTrip(raw, &in) {
			out.GuardOperations = &serverGuardOperations{
				Contract: in.Contract, State: in.Journal, Durable: in.Durable, UnresolvedOutcomeUnknown: in.UnresolvedOutcomeUnknown,
				UnackedResults: in.UnackedResults, InFlight: in.InFlight, JournalBytes: in.JournalBytes,
				JournalBudgetBytes: in.JournalBudgetBytes, Reason: in.Reason,
			}
		}
	}
	if raw, ok := worker.Capabilities["guard_posture"]; ok {
		var in serverGuardPosture
		if roundTrip(raw, &in) {
			out.GuardPosture = &in
		}
	}
	return out
}

func roundTrip(in any, out any) bool {
	raw, err := json.Marshal(in)
	return err == nil && json.Unmarshal(raw, out) == nil
}

// capabilitiesByWorker reads the tenant's workers once. A read failure leaves
// the section absent instead of failing the inventory.
func (h serverRuntimeHandlers) capabilitiesByWorker(ctx context.Context, tenantID string) map[string]*serverRuntimeCapabilities {
	if h.workers == nil {
		return nil
	}
	workers, err := h.workers.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		slog.Warn("server_capabilities_read_failed", "error", err.Error())
		return nil
	}
	out := make(map[string]*serverRuntimeCapabilities, len(workers))
	for _, worker := range workers {
		// A worker of another tenant is never served, whatever a server row names.
		if strings.TrimSpace(worker.TenantID) == tenantID {
			out[strings.TrimSpace(worker.ID)] = serverCapabilitiesFromWorker(worker)
		}
	}
	return out
}

func (h serverRuntimeHandlers) capabilitiesForWorker(ctx context.Context, tenantID, workerID string) *serverRuntimeCapabilities {
	if h.workers == nil || strings.TrimSpace(workerID) == "" {
		return nil
	}
	worker, err := h.workers.GetWorker(ctx, tenantID, strings.TrimSpace(workerID))
	if err != nil || worker == nil || strings.TrimSpace(worker.TenantID) != tenantID {
		return nil
	}
	return serverCapabilitiesFromWorker(*worker)
}
