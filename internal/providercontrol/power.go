package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

// PowerDesiredSpecSchemaVersion identifies the desired spec of an in-place
// managed power reconcile. Stop and start keep the exact resource generation:
// no resource is created, replaced, or deleted, and decommission remains the
// only destroy.
const PowerDesiredSpecSchemaVersion = "techstack/provider-power-spec/v1"

// PowerConvergenceTimeout bounds read-only convergence polling after the one
// side-effecting power step. A guest that never reaches the desired state
// fails the operation as a retryable provider timeout instead of polling
// forever; the owner starts a new semantic attempt.
const PowerConvergenceTimeout = 10 * time.Minute

// GuestPowerOffRequest names one exact managed runtime whose guest OS must
// shut down gracefully through the control-plane execution channel. It is used
// by adapters whose provider has no allocation-preserving power-off action.
type GuestPowerOffRequest struct {
	TenantID        string
	LeaseID         string
	RuntimeServerID string
	// PublicIPv4 is the provider-observed address of the exact graph. The
	// executor must refuse a channel target that resolves elsewhere.
	PublicIPv4 string
}

// GuestPowerOffExecutor requests a graceful guest shutdown. It must be
// idempotent: a guest that is already down cannot be reached and the
// provider read decides convergence.
type GuestPowerOffExecutor interface {
	PowerOffGuest(context.Context, GuestPowerOffRequest) error
}

// PowerState is the provider-neutral desired power state.
type PowerState string

const (
	PowerStateRunning PowerState = "running"
	PowerStateStopped PowerState = "stopped"
)

// Valid reports whether the state is one of the two admitted targets.
func (s PowerState) Valid() bool {
	return s == PowerStateRunning || s == PowerStateStopped
}

// PowerDesiredSpec is the canonical reconcile payload. It carries no provider
// handle or credential: the exact targets are already sealed in the command.
type PowerDesiredSpec struct {
	SchemaVersion string     `json:"schema_version"`
	ProviderID    string     `json:"provider_id"`
	PowerState    PowerState `json:"power_state"`
}

// NewPowerDesiredSpec returns the canonical JSON payload for one target state.
func NewPowerDesiredSpec(providerID string, state PowerState) (json.RawMessage, error) {
	providerID = strings.TrimSpace(providerID)
	if !canonicalProviderID(providerID) || !state.Valid() {
		return nil, fmt.Errorf("%w: power desired spec requires a canonical provider and target state", ErrInvalidRequest)
	}
	payload, err := json.Marshal(PowerDesiredSpec{
		SchemaVersion: PowerDesiredSpecSchemaVersion, ProviderID: providerID, PowerState: state,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode power desired spec: %v", ErrInvalidRequest, err)
	}
	return canonicalJSON(payload)
}

// DesiredSpecDigest returns the ledger digest of a canonical desired spec.
func DesiredSpecDigest(payload json.RawMessage) string {
	return sha256Digest(payload)
}

// LoadPowerDesiredSpec reads the exact digest-bound desired spec of a
// reconcile command inside a tenant-scoped read-only transaction and rejects
// any payload that is not a power spec for the command's provider.
func LoadPowerDesiredSpec(ctx context.Context, db *sql.DB, command providerexecutor.Command) (PowerDesiredSpec, error) {
	if db == nil || command.Operation != providerexecutor.OperationReconcile ||
		strings.TrimSpace(command.DesiredSpecRef) == "" || strings.TrimSpace(command.DesiredSpecHash) == "" {
		return PowerDesiredSpec{}, fmt.Errorf("%w: power reconcile requires a sealed desired spec", ErrInvalidRequest)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PowerDesiredSpec{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, command.TenantID); err != nil {
		return PowerDesiredSpec{}, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT spec_json::text
		FROM provider_desired_spec_revisions
		WHERE tenant_id = $1 AND lease_id = $2 AND spec_ref = $3 AND spec_digest = $4
	`, command.TenantID, command.LeaseID, command.DesiredSpecRef, command.DesiredSpecHash).Scan(&raw); err != nil {
		return PowerDesiredSpec{}, fmt.Errorf("%w: load power desired spec: %v", ErrInvalidRequest, err)
	}
	if err := tx.Commit(); err != nil {
		return PowerDesiredSpec{}, err
	}
	canonical, err := canonicalJSON(raw)
	if err != nil || sha256Digest(canonical) != command.DesiredSpecHash {
		return PowerDesiredSpec{}, fmt.Errorf("%w: power desired spec digest mismatch", ErrInvalidRequest)
	}
	var spec PowerDesiredSpec
	if err := json.Unmarshal(canonical, &spec); err != nil ||
		spec.SchemaVersion != PowerDesiredSpecSchemaVersion ||
		spec.ProviderID != command.ProviderID || !spec.PowerState.Valid() {
		return PowerDesiredSpec{}, fmt.Errorf("%w: desired spec is not a power spec for %s", ErrInvalidRequest, command.ProviderID)
	}
	return spec, nil
}

// PowerReconcileResources returns the exact command target graph as present,
// cleanup-required bindings. A power transition never changes identity, so
// every reconcile receipt carries precisely these resources.
func PowerReconcileResources(request providerexecutor.ExecutionRequest) []providerexecutor.ResourceBinding {
	if len(request.Previous.Resources) > 0 {
		return append([]providerexecutor.ResourceBinding(nil), request.Previous.Resources...)
	}
	resources := make([]providerexecutor.ResourceBinding, 0, len(request.Command.Targets))
	for _, target := range request.Command.Targets {
		resources = append(resources, providerexecutor.ResourceBinding{
			BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef,
			ParentBindingID: target.ParentBindingID, OwnershipHash: target.OwnershipHash,
			Disposition: target.Disposition,
			Observation: providerexecutor.ObservationPresent,
			Cleanup:     providerexecutor.CleanupRequired,
		})
	}
	return resources
}

// PowerConvergenceExpired reports whether read-only polling has exceeded
// PowerConvergenceTimeout since the resources_bound phase began.
func PowerConvergenceExpired(previous providerexecutor.Receipt, now time.Time) bool {
	entered := previous.PhaseEnteredAt
	if entered.IsZero() {
		entered = previous.IssuedAt
	}
	return !entered.IsZero() && now.UTC().After(entered.UTC().Add(PowerConvergenceTimeout))
}

// PowerPending keeps the operation at resources_bound with its exact graph.
func PowerPending(resources []providerexecutor.ResourceBinding) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: resources,
	}
}

// PowerConverged seals the reconcile as present with its exact graph.
func PowerConverged(resources []providerexecutor.ResourceBinding) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: resources,
	}
}

// PowerFailed terminates the reconcile while retaining every live handle for
// the later decommission; the resources are still present and billable.
func PowerFailed(code string, retryable bool, resources []providerexecutor.ResourceBinding) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
		Resources: resources,
		Reason:    &providerexecutor.Reason{Code: code, Retryable: retryable},
	}
}
