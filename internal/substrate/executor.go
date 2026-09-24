package substrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const AdapterID = "proxmox-substrate-v1"
const AdapterManifestHash = "sha256:d0d36d0ce8eca8c262240fb4e3d08603acdb9fd2ebd0182ef1584b9a4c701876"

// ExecutionSpec is retained as the exact provider desired-spec payload by
// Techstack. These closed actions cannot invoke shells or arbitrary API paths.
type ExecutionSpec struct {
	Action   string        `json:"action"`
	Guest    GuestSpec     `json:"guest,omitempty"`
	Identity GuestIdentity `json:"identity,omitempty"`
	Image    Image         `json:"image,omitempty"`
	Storage  string        `json:"storage,omitempty"`
	DiskGiB  int           `json:"disk_gib,omitempty"`
	Isolated bool          `json:"isolated,omitempty"`
}

// Executor is installed exclusively in the enrolled substrate Guard. The
// authenticated worker channel supplies the exact admitted execution; raw
// caller payloads never reach this boundary through a public admin endpoint.
type Executor struct {
	Client    *Client
	WorkerID  string
	TenantID  string
	bootstrap func(context.Context) ([]byte, error)
}

func (e Executor) ExecuteWorker(ctx context.Context, command providerexecutor.WorkerExecution) providerexecutor.WorkerExecutionResult {
	result := providerexecutor.WorkerExecutionResult{InvocationID: command.InvocationID, CommandDigest: command.Command.CommandDigest}
	fail := func(code string) providerexecutor.WorkerExecutionResult {
		result.ErrorCode = code
		result.Complete = true
		return result
	}
	if e.Client == nil || command.WorkerID != e.WorkerID || command.Command.TenantID != e.TenantID || command.Command.AdapterID != AdapterID || command.Command.ProviderID != "proxmox" {
		return fail("substrate_binding_mismatch")
	}
	if err := command.Validate(time.Now()); err != nil {
		return fail("substrate_command_invalid")
	}
	ctx, cancel := context.WithDeadline(ctx, command.ExpiresAt)
	defer cancel()
	if command.Phase == providerexecutor.WorkerPhaseObserve && command.TaskRef != "" {
		task, err := e.Client.ObserveTask(ctx, command.TaskRef)
		if err != nil {
			return fail("substrate_observation_failed")
		}
		result.TaskRef = command.TaskRef
		result.Complete = task.Complete()
		result.Succeeded = task.Succeeded()
		result.Observation, _ = json.Marshal(task)
		return result
	}
	if command.Command.Operation == providerexecutor.OperationDecommission {
		return e.decommission(ctx, command, result)
	}
	var spec ExecutionSpec
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&spec) != nil {
		return fail("substrate_spec_invalid")
	}
	provisionStage := spec.Action == "provision"
	if !provisionStage && command.Phase == providerexecutor.WorkerPhaseObserve && command.Stage == "guest" {
		spec.Action = "guest"
	}
	if provisionStage {
		if command.Command.Operation != providerexecutor.OperationProvision {
			return fail("substrate_action_not_admitted")
		}
		if command.Phase == providerexecutor.WorkerPhaseObserve {
			if command.Stage != "guest" {
				return fail("substrate_action_not_admitted")
			}
			spec.Action = "guest"
			spec.Identity = spec.Guest.GuestIdentity
		} else {
			switch command.Stage {
			case "image_import":
				spec.Action = "image_import"
				spec.Image = spec.Guest.ImageImport.Image
				spec.Storage = spec.Guest.ImageImport.Storage
			case "create":
				if command.Prerequisite == nil || !command.Prerequisite.Accepted {
					return fail("substrate_image_receipt_required")
				}
				var imported ImageImport
				if json.Unmarshal(command.Prerequisite.Observation, &imported) != nil || imported.Image != spec.Guest.ImageImport.Image || imported.Storage != spec.Guest.ImageImport.Storage || imported.TaskRef != command.Prerequisite.TaskRef {
					return fail("substrate_image_receipt_mismatch")
				}
				spec.Action = "create"
				spec.Guest.ImageImport = imported
			case "resize":
				spec.Action = "resize"
				spec.Identity = spec.Guest.GuestIdentity
				spec.DiskGiB = spec.Guest.DiskGiB
			case "start":
				spec.Action = "start"
				spec.Identity = spec.Guest.GuestIdentity
			default:
				return fail("substrate_stage_invalid")
			}
		}
	}
	if !operationAllows(command.Command.Operation, command.Phase, spec.Action) && !(provisionStage && command.Command.Operation == providerexecutor.OperationProvision && command.Stage != "" && (spec.Action == "image_import" || spec.Action == "resize" || spec.Action == "start")) {
		return fail("substrate_action_not_admitted")
	}
	var submission Submission
	var observation any
	var err error
	switch spec.Action {
	case "create":
		var bootstrap [][]byte
		if spec.Guest.Enrollment {
			if e.bootstrap == nil {
				return fail("substrate_bootstrap_unavailable")
			}
			payload, fetchErr := e.bootstrap(ctx)
			if fetchErr != nil {
				return fail("substrate_bootstrap_unavailable")
			}
			bootstrap = append(bootstrap, payload)
		}
		submission, err = e.Client.SubmitCreate(ctx, spec.Guest, bootstrap...)
	case "image_import":
		var imported ImageImport
		imported, err = e.Client.SubmitImageImport(ctx, spec.Storage, spec.Image)
		submission.TaskRef = imported.TaskRef
		observation = imported
	case "resize":
		err = e.Client.ResizeGuestDisk(ctx, spec.Identity, spec.DiskGiB)
	case "start", "stop", "shutdown", "delete":
		submission, err = e.Client.SubmitLifecycle(ctx, spec.Identity, spec.Action)
	case "set_isolation":
		err = e.Client.SetNetworkIsolation(ctx, spec.Identity, spec.Isolated)
	case "inventory":
		observation, err = e.Client.Inventory(ctx)
	case "guest":
		observation, err = e.Client.ObserveGuest(ctx, spec.Identity)
	default:
		err = ErrCapability
	}
	if err != nil {
		if errors.Is(err, ErrOwnership) {
			return fail("substrate_ownership_denied")
		}
		if errors.Is(err, ErrConflict) {
			return fail("substrate_conflict")
		}
		return fail("substrate_execution_failed")
	}
	result.Accepted = command.Phase == providerexecutor.WorkerPhaseSubmit
	result.TaskRef = submission.TaskRef
	result.Complete = submission.TaskRef == "" && !submission.Recovered
	result.Succeeded = result.Complete
	if observation == nil {
		observation = submission
	}
	result.Observation, _ = json.Marshal(observation)
	return result
}

func (e Executor) decommission(ctx context.Context, command providerexecutor.WorkerExecution, result providerexecutor.WorkerExecutionResult) providerexecutor.WorkerExecutionResult {
	result.Complete = true
	if len(command.Command.Targets) != 1 {
		result.ErrorCode = "substrate_exact_target_required"
		return result
	}
	target := command.Command.Targets[0]
	parts := strings.Split(target.NativeRef, "/")
	if len(parts) != 2 {
		result.ErrorCode = "substrate_exact_target_required"
		return result
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil || id < e.Client.cfg.MinGuestID || id > e.Client.cfg.MaxGuestID {
		result.ErrorCode = "substrate_ownership_denied"
		return result
	}
	guest, err := e.Client.Guest(ctx, id)
	if errors.Is(err, ErrNotFound) {
		result.Accepted = command.Phase == providerexecutor.WorkerPhaseSubmit
		result.Succeeded = true
		result.Observation, _ = json.Marshal(GuestObservation{ID: id})
		return result
	}
	if err != nil {
		result.ErrorCode = "substrate_observation_failed"
		return result
	}
	identity := GuestIdentity{ID: id, Name: guest.Name}
	for _, tag := range strings.Split(guest.Tags, ";") {
		if operationTag.MatchString(tag) {
			if identity.OperationTag != "" {
				result.ErrorCode = "substrate_ownership_denied"
				return result
			}
			identity.OperationTag = tag
		}
	}
	payload, _ := json.Marshal(identity)
	hash := sha256.Sum256(payload)
	if target.OwnershipHash != "sha256:"+hex.EncodeToString(hash[:]) || e.Client.validateIdentity(identity) != nil || !owns(identity, guest) {
		result.ErrorCode = "substrate_ownership_denied"
		return result
	}
	if command.Phase == providerexecutor.WorkerPhaseObserve {
		result.Succeeded = true
		result.Observation, _ = json.Marshal(GuestObservation{Present: true, ID: id, Name: guest.Name, Status: guest.Status, Locked: guest.Lock != ""})
		return result
	}
	action := "delete"
	if command.Stage == "stop" {
		action = "stop"
	} else if command.Stage != "" && command.Stage != "delete" {
		result.ErrorCode = "substrate_stage_invalid"
		return result
	}
	submitted, err := e.Client.SubmitLifecycle(ctx, identity, action)
	if err != nil {
		result.ErrorCode = "substrate_delete_failed"
		return result
	}
	result.Accepted = true
	result.TaskRef = submitted.TaskRef
	result.Complete = submitted.TaskRef == ""
	result.Succeeded = result.Complete
	return result
}

func operationAllows(operation providerexecutor.Operation, phase providerexecutor.WorkerPhase, action string) bool {
	if phase == providerexecutor.WorkerPhaseObserve {
		return action == "inventory" || action == "guest"
	}
	switch operation {
	case providerexecutor.OperationProvision:
		return action == "create"
	case providerexecutor.OperationReconcile:
		return action == "image_import" || action == "resize" || action == "start" || action == "stop" || action == "shutdown" || action == "set_isolation"
	case providerexecutor.OperationDecommission:
		return action == "delete"
	default:
		return false
	}
}

func (e Executor) ExecuteWorkerWithBootstrap(ctx context.Context, command providerexecutor.WorkerExecution, fetch func(context.Context) ([]byte, error)) providerexecutor.WorkerExecutionResult {
	e.bootstrap = fetch
	return e.ExecuteWorker(ctx, command)
}
