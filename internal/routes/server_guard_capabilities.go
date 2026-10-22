package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardcompat"
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

// ServerRoundTripReader reads the persisted authenticated command/result round
// trips of a tenant's Guards, keyed by agent ID. agentcontrol.Hub satisfies it.
// Nil leaves every server without round-trip evidence, so none reads as
// Connected.
type ServerRoundTripReader interface {
	CommandRoundTripsForTenant(ctx context.Context, tenantID string) (map[string]agentcontrol.CommandRoundTrip, error)
}

// Round-trip states of the capabilities section. Only verified lets the
// Connected readiness fact pass; every other state demotes it.
const (
	serverRoundTripVerified   = "verified"
	serverRoundTripStale      = "stale"
	serverRoundTripSuperseded = "superseded"
	serverRoundTripAbsent     = "absent"
)

// serverCommandRoundTrip is the Connected evidence of one server: whether the
// last command the Guard answered under the binding contract was accepted
// under its current credential generation recently enough. A heartbeat is
// never part of it.
type serverCommandRoundTrip struct {
	State          string     `json:"state"`
	ReasonCode     string     `json:"reason_code,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	Generation     int64      `json:"generation,omitempty"`
	// Transport is https (bound HTTPS hub) or mtls (gRPC listener).
	Transport string `json:"transport,omitempty"`
}

// commandRoundTripEvidence classifies the persisted round trip at now against
// the identity the Guard holds now: its credential generation for an HTTPS
// round trip, the certificate serial of its current mTLS session for an mTLS
// one (empty when no session is present).
func commandRoundTripEvidence(trip *agentcontrol.CommandRoundTrip, currentGeneration int64, currentMTLSSerial string, now time.Time) *serverCommandRoundTrip {
	if trip == nil {
		return &serverCommandRoundTrip{State: serverRoundTripAbsent, ReasonCode: "no_authenticated_round_trip"}
	}
	acknowledged := trip.AcknowledgedAt.UTC()
	out := &serverCommandRoundTrip{AcknowledgedAt: &acknowledged, Generation: trip.Generation, Transport: trip.Transport}
	switch {
	case trip.Transport == agentcontrol.RoundTripTransportMTLS && (currentMTLSSerial == "" || trip.CredentialRef != currentMTLSSerial):
		out.State, out.ReasonCode = serverRoundTripSuperseded, "mtls_identity_changed"
	case trip.Transport != agentcontrol.RoundTripTransportMTLS && (currentGeneration <= 0 || trip.Generation != currentGeneration):
		out.State, out.ReasonCode = serverRoundTripSuperseded, "credential_generation_changed"
	case acknowledged.After(now.Add(guardcompat.ResultClockMargin)):
		out.State, out.ReasonCode = serverRoundTripStale, "round_trip_time_invalid"
	case now.Sub(acknowledged) > guardcompat.CommandRoundTripFreshness:
		out.State, out.ReasonCode = serverRoundTripStale, "round_trip_stale"
	default:
		out.State = serverRoundTripVerified
	}
	return out
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
	// CommandRoundTrip is the persisted Connected evidence; see
	// serverCommandRoundTrip.
	CommandRoundTrip     *serverCommandRoundTrip `json:"command_round_trip"`
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

// applyRoundTrips attaches the round-trip evidence of each worker. A read
// failure leaves every worker absent, which demotes Connected instead of
// failing the inventory.
func (h serverRuntimeHandlers) applyRoundTrips(ctx context.Context, tenantID string, capabilities map[string]*serverRuntimeCapabilities) {
	if len(capabilities) == 0 {
		return
	}
	var trips map[string]agentcontrol.CommandRoundTrip
	if h.roundTrips != nil {
		read, err := h.roundTrips.CommandRoundTripsForTenant(ctx, tenantID)
		if err != nil {
			slog.Warn("server_round_trip_read_failed", "error", err.Error())
		} else {
			trips = read
		}
	}
	now := h.now().UTC()
	for workerID, item := range capabilities {
		if item == nil {
			continue
		}
		var trip *agentcontrol.CommandRoundTrip
		if found, ok := trips[workerID]; ok {
			trip = &found
		}
		serial := ""
		if h.currentMTLSIdentity != nil {
			serial = strings.TrimSpace(h.currentMTLSIdentity(tenantID, workerID))
		}
		item.CommandRoundTrip = commandRoundTripEvidence(trip, item.CredentialGeneration, serial, now)
	}
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
	h.applyRoundTrips(ctx, tenantID, out)
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
	out := serverCapabilitiesFromWorker(*worker)
	h.applyRoundTrips(ctx, tenantID, map[string]*serverRuntimeCapabilities{strings.TrimSpace(worker.ID): out})
	return out
}
