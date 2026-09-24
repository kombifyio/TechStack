// Package substrateruntime composes customer-substrate execution into the
// existing provider-control coordinator. It has no commercial provider policy.
package substrateruntime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/providerevidence"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/google/uuid"
)

type WorkerTransport interface {
	SendWorkerExecution(context.Context, providerexecutor.WorkerExecution) (providerexecutor.WorkerExecutionResult, error)
	ReadWorkerExecutionResult(context.Context, string, string) (providerexecutor.WorkerExecutionResult, bool, error)
}

type Adapter struct {
	db        *sql.DB
	transport WorkerTransport
}

var errTaskFailed = errors.New("substrate native task failed")

func NewAdapter(database *sql.DB, transport WorkerTransport) (*Adapter, error) {
	if database == nil || transport == nil {
		return nil, errors.New("substrate adapter requires durable authority and Guard transport")
	}
	return &Adapter{db: database, transport: transport}, nil
}

func (a *Adapter) CrashRecoveryCapability() providercontrol.CrashRecoveryCapability {
	return providercontrol.CrashRecoveryCapability{
		AdapterManifestHash: substrate.AdapterManifestHash, Mode: providercontrol.CrashRecoveryProviderCorrelation, PerHeadInvocationKey: true, ProviderPersistedCorrelation: true, UniqueCorrelation: true, RecoveryByCorrelation: true,
	}
}

func (a *Adapter) ExecuteCrashRecoverableMutation(ctx context.Context, inv providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	if inv.Request.Command.Operation == providerexecutor.OperationDecommission {
		return a.decommission(ctx, inv, false)
	}
	if inv.Request.Command.Operation != providerexecutor.OperationProvision {
		return a.executeSingle(ctx, inv, false)
	}
	spec, payload, nativeRef, err := a.spec(ctx, inv.Request.Command)
	if err != nil || spec.Action != "provision" {
		return failure(providerexecutor.ReasonCodeProviderInvalidSpec, nil)
	}
	resources := append([]providerexecutor.ResourceBinding(nil), inv.Request.Previous.Resources...)
	imported, err := a.submit(ctx, inv, payload, "image_import", nil)
	if err != nil {
		return pending(inv, resources)
	}
	if imported.ErrorCode != "" {
		return failure(providerexecutor.ReasonCodeProviderTransient, resources)
	}
	ready, err := a.observeTask(ctx, inv, payload, imported)
	if errors.Is(err, errTaskFailed) {
		return failure(providerexecutor.ReasonCodeProviderInvalidSpec, resources)
	}
	if err != nil || !ready {
		return pending(inv, resources)
	}
	created, err := a.submit(ctx, inv, payload, "create", &imported)
	if err != nil {
		return pending(inv, resources)
	}
	if created.Accepted {
		resources = []providerexecutor.ResourceBinding{binding(inv.Request.Command, spec.Guest.GuestIdentity, nativeRef)}
	}
	if created.ErrorCode != "" {
		return failure(providerexecutor.ReasonCodeProviderPartialCreate, resources)
	}
	ready, err = a.observeTask(ctx, inv, payload, created)
	if errors.Is(err, errTaskFailed) {
		return failure(providerexecutor.ReasonCodeProviderPartialCreate, resources)
	}
	if err != nil || !ready {
		return pending(inv, resources)
	}
	// Recovered create has no UPID after loss of its original response. Its
	// authoritative config/lock/disk observation decides readiness instead.
	guest, err := a.observeGuest(ctx, inv, payload)
	if err != nil || !guest.Present || guest.Locked || !guest.DiskAttached {
		return pending(inv, resources)
	}
	resized, err := a.submit(ctx, inv, payload, "resize", nil)
	if err != nil {
		return pending(inv, resources)
	}
	if resized.ErrorCode != "" {
		return failure(providerexecutor.ReasonCodeProviderPartialCreate, resources)
	}
	if !spec.Guest.Isolated {
		started, err := a.submit(ctx, inv, payload, "start", nil)
		if err != nil {
			return pending(inv, resources)
		}
		if started.ErrorCode != "" {
			return failure(providerexecutor.ReasonCodeProviderPartialCreate, resources)
		}
		ready, err = a.observeTask(ctx, inv, payload, started)
		if errors.Is(err, errTaskFailed) {
			return failure(providerexecutor.ReasonCodeProviderPartialCreate, resources)
		}
		if err != nil || !ready {
			return pending(inv, resources)
		}
	}
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound, Resources: resources}
}

func (a *Adapter) ExecuteCrashRecoverableReadOnly(ctx context.Context, inv providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	if inv.Request.Command.Operation == providerexecutor.OperationDecommission {
		return a.decommission(ctx, inv, true)
	}
	if inv.Request.Command.Operation != providerexecutor.OperationProvision {
		return a.executeSingle(ctx, inv, true)
	}
	spec, payload, _, err := a.spec(ctx, inv.Request.Command)
	if err != nil {
		return failure(providerexecutor.ReasonCodeProviderInvalidSpec, inv.Request.Previous.Resources)
	}
	guest, err := a.observeGuest(ctx, inv, payload)
	if err != nil || !guest.Present || guest.Locked || !guest.DiskAttached {
		return pending(inv, inv.Request.Previous.Resources)
	}
	if !spec.Guest.Isolated && (guest.Status != "running" || len(guest.Addresses) == 0) {
		return pending(inv, inv.Request.Previous.Resources)
	}
	resources := append([]providerexecutor.ResourceBinding(nil), inv.Request.Previous.Resources...)
	for i := range resources {
		resources[i].Observation = providerexecutor.ObservationPresent
	}
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent, Resources: resources}
}

func (a *Adapter) submit(ctx context.Context, inv providercontrol.AdapterInvocation, payload json.RawMessage, stage string, prerequisite *providerexecutor.WorkerExecutionResult) (providerexecutor.WorkerExecutionResult, error) {
	id := inv.Request.Command.OperationID + "-" + stage
	result, found, err := a.transport.ReadWorkerExecutionResult(ctx, inv.Request.Command.TenantID, id)
	if err != nil {
		return result, err
	}
	if found {
		if result.CommandDigest != inv.Request.Command.CommandDigest {
			return result, errors.New("retained substrate stage command mismatch")
		}
		return result, nil
	}
	return a.transport.SendWorkerExecution(ctx, a.execution(inv, payload, providerexecutor.WorkerPhaseSubmit, id, stage, "", prerequisite))
}

func (a *Adapter) decommission(ctx context.Context, inv providercontrol.AdapterInvocation, readOnly bool) providerexecutor.ExecutionResult {
	if len(inv.Request.Command.Targets) != 1 {
		return failure(providerexecutor.ReasonCodeProviderInvalidSpec, inv.Request.Previous.Resources)
	}
	target := inv.Request.Command.Targets[0]
	resources := append([]providerexecutor.ResourceBinding(nil), inv.Request.Previous.Resources...)
	if len(resources) == 0 {
		resources = []providerexecutor.ResourceBinding{{BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef, OwnershipHash: target.OwnershipHash, Disposition: target.Disposition, Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupRequired}}
	}
	if !readOnly {
		stopped, err := a.submit(ctx, inv, nil, "stop", nil)
		if err != nil {
			return pending(inv, resources)
		}
		if stopped.ErrorCode != "" {
			return failure(providerexecutor.ReasonCodeProviderConflict, resources)
		}
		ready, err := a.observeTask(ctx, inv, nil, stopped)
		if errors.Is(err, errTaskFailed) {
			return failure(providerexecutor.ReasonCodeProviderConflict, resources)
		}
		if err != nil || !ready {
			return pending(inv, resources)
		}
	}
	phase := providerexecutor.WorkerPhaseSubmit
	id := inv.Request.Command.OperationID + "-delete"
	if readOnly {
		phase = providerexecutor.WorkerPhaseObserve
		id = inv.Key + "-absence-" + uuid.NewString()
	}
	result, err := a.transport.SendWorkerExecution(ctx, a.execution(inv, nil, phase, id, "", "", nil))
	if err != nil {
		return pending(inv, resources)
	}
	if result.ErrorCode != "" {
		return failure(providerexecutor.ReasonCodeProviderConflict, resources)
	}
	if !readOnly {
		resources[0].Cleanup = providerexecutor.CleanupPending
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted, Resources: resources}
	}
	if inv.Request.Previous.Phase == providerexecutor.PhaseDeleteAccepted {
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending, Resources: resources}
	}
	var observed substrate.GuestObservation
	if json.Unmarshal(result.Observation, &observed) != nil || observed.Present {
		return pending(inv, resources)
	}
	recorder, err := providerevidence.NewRecorder(a.db)
	if err != nil {
		return pending(inv, resources)
	}
	evidence, err := recorder.Record(ctx, inv.Request.Command, target, providerevidence.Observation{Endpoint: "proxmox-node-local:/qemu", StatusCode: 200, CollectedAt: time.Now().UTC(), AuthoritativeListAbsent: target.NativeRef})
	if err != nil {
		return pending(inv, resources)
	}
	resources[0].Observation = providerexecutor.ObservationAbsent
	resources[0].Cleanup = providerexecutor.CleanupComplete
	resources[0].Evidence = []providerexecutor.Evidence{evidence}
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent, Resources: resources}
}

func (a *Adapter) observeTask(ctx context.Context, inv providercontrol.AdapterInvocation, payload json.RawMessage, submitted providerexecutor.WorkerExecutionResult) (bool, error) {
	if submitted.TaskRef == "" {
		return submitted.ErrorCode == "", nil
	}
	result, err := a.transport.SendWorkerExecution(ctx, a.execution(inv, payload, providerexecutor.WorkerPhaseObserve, inv.Key+"-observe-"+uuid.NewString(), "task", submitted.TaskRef, nil))
	if err != nil {
		return false, err
	}
	if result.ErrorCode != "" || result.Complete && !result.Succeeded {
		return false, errTaskFailed
	}
	return result.Complete && result.Succeeded, nil
}

func (a *Adapter) observeGuest(ctx context.Context, inv providercontrol.AdapterInvocation, payload json.RawMessage) (substrate.GuestObservation, error) {
	result, err := a.transport.SendWorkerExecution(ctx, a.execution(inv, payload, providerexecutor.WorkerPhaseObserve, inv.Key+"-guest-"+uuid.NewString(), "guest", "", nil))
	if err != nil {
		return substrate.GuestObservation{}, err
	}
	if result.ErrorCode != "" {
		return substrate.GuestObservation{}, errors.New("substrate guest observation failed")
	}
	var observed substrate.GuestObservation
	err = json.Unmarshal(result.Observation, &observed)
	return observed, err
}

func (a *Adapter) execution(inv providercontrol.AdapterInvocation, payload json.RawMessage, phase providerexecutor.WorkerPhase, id, stage, task string, prerequisite *providerexecutor.WorkerExecutionResult) providerexecutor.WorkerExecution {
	parts := strings.Split(strings.TrimPrefix(inv.Request.Command.ConnectionRef, "provider-connection://substrate/"), "/")
	worker := ""
	if len(parts) == 2 && parts[1] == inv.Request.Command.LeaseID {
		worker = parts[0]
	}
	return providerexecutor.WorkerExecution{Command: inv.Request.Command, InvocationID: id, WorkerID: worker, HeadSequence: inv.Request.Previous.Sequence, HeadDigest: inv.Request.Previous.ReceiptDigest, Phase: phase, Stage: stage, Payload: payload, TaskRef: task, Prerequisite: prerequisite, ExpiresAt: time.Now().UTC().Add(45 * time.Second)}
}

func (a *Adapter) spec(ctx context.Context, command providerexecutor.Command) (substrate.ExecutionSpec, json.RawMessage, string, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return substrate.ExecutionSpec{}, nil, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, command.TenantID); err != nil {
		return substrate.ExecutionSpec{}, nil, "", err
	}
	var payload []byte
	var nativeRef string
	err = tx.QueryRowContext(ctx, `SELECT spec.spec_json,lease.lease_json #>> '{resource,engine_vm_id}' FROM provider_desired_spec_revisions spec JOIN techstack_vm_leases lease ON lease.tenant_id=spec.tenant_id AND lease.id=spec.lease_id WHERE spec.tenant_id=$1 AND spec.lease_id=$2 AND spec.spec_ref=$3 AND spec.spec_digest=$4 AND lease.lease_revision=$5`, command.TenantID, command.LeaseID, command.DesiredSpecRef, command.DesiredSpecHash, command.LeaseRevision).Scan(&payload, &nativeRef)
	if err != nil {
		return substrate.ExecutionSpec{}, nil, "", err
	}
	var data any
	if json.Unmarshal(payload, &data) != nil {
		return substrate.ExecutionSpec{}, nil, "", errors.New("invalid desired spec")
	}
	payload, err = json.Marshal(data)
	if err != nil {
		return substrate.ExecutionSpec{}, nil, "", err
	}
	var spec substrate.ExecutionSpec
	err = json.Unmarshal(payload, &spec)
	return spec, payload, nativeRef, err
}

func (a *Adapter) executeSingle(ctx context.Context, inv providercontrol.AdapterInvocation, readOnly bool) providerexecutor.ExecutionResult {
	resources := append([]providerexecutor.ResourceBinding(nil), inv.Request.Previous.Resources...)
	for _, target := range inv.Request.Command.Targets {
		if len(resources) == 0 {
			resources = append(resources, providerexecutor.ResourceBinding{BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef, OwnershipHash: target.OwnershipHash, Disposition: target.Disposition, Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupRequired})
		}
	}
	spec, payload, _, err := a.spec(ctx, inv.Request.Command)
	if err != nil {
		return failure(providerexecutor.ReasonCodeProviderInvalidSpec, resources)
	}
	phase := providerexecutor.WorkerPhaseSubmit
	stage := ""
	if readOnly {
		phase = providerexecutor.WorkerPhaseObserve
		stage = "guest"
	}
	id := inv.Key
	if readOnly {
		id += "-" + uuid.NewString()
	}
	result, err := a.transport.SendWorkerExecution(ctx, a.execution(inv, payload, phase, id, stage, "", nil))
	if err != nil {
		return pending(inv, resources)
	}
	if result.ErrorCode != "" {
		return failure(providerexecutor.ReasonCodeProviderTransient, resources)
	}
	if result.TaskRef != "" {
		ready, err := a.observeTask(ctx, inv, payload, result)
		if errors.Is(err, errTaskFailed) {
			return failure(providerexecutor.ReasonCodeProviderInvalidSpec, resources)
		}
		if err != nil || !ready {
			return pending(inv, resources)
		}
	}
	if !readOnly && inv.Request.Command.Operation == providerexecutor.OperationReconcile {
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound, Resources: resources}
	}
	if readOnly {
		var observed substrate.GuestObservation
		if json.Unmarshal(result.Observation, &observed) != nil || !observed.Present {
			return pending(inv, resources)
		}
		if (spec.Action == "start" && observed.Status != "running") || ((spec.Action == "stop" || spec.Action == "shutdown") && observed.Status != "stopped") {
			return pending(inv, resources)
		}
	}
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent, Resources: resources}
}

func pending(inv providercontrol.AdapterInvocation, resources []providerexecutor.ResourceBinding) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: inv.Request.Previous.Phase, Resources: resources}
}
func failure(code string, resources []providerexecutor.ResourceBinding) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed, Resources: resources, Reason: &providerexecutor.Reason{Code: code, Retryable: false}}
}
func binding(command providerexecutor.Command, identity substrate.GuestIdentity, nativeRef string) providerexecutor.ResourceBinding {
	payload, _ := json.Marshal(identity)
	hash := sha256.Sum256(payload)
	return providerexecutor.ResourceBinding{BindingID: "guest-" + command.ResourceGenerationID, Kind: "virtual_machine", NativeRef: nativeRef, OwnershipHash: "sha256:" + hex.EncodeToString(hash[:]), Disposition: providerexecutor.DispositionDelete, Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupRequired}
}
