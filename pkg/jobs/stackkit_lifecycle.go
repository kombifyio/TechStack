package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
)

const (
	StackKitLifecyclePlan           = "plan"
	StackKitLifecycleGenerate       = "generate"
	StackKitLifecycleApply          = "apply"
	StackKitLifecycleVerify         = "verify"
	StackKitLifecycleUpgrade        = "upgrade"
	StackKitLifecycleDriftDetect    = "drift_detect"
	StackKitLifecycleDriftReconcile = "drift_reconcile"
	StackKitLifecycleInit           = "init"
	StackKitLifecycleServiceStart   = "service_start"
	StackKitLifecycleServiceStop    = "service_stop"
	StackKitLifecycleServiceRestart = "service_restart"
	StackKitLifecycleServiceLogs    = "service_logs"

	defaultStackKitNodeWorkspace = "/opt/stackkit"
	stackKitReadCommandTimeout   = 5 * time.Minute
	stackKitWriteCommandTimeout  = 12 * time.Minute

	// Managed rollout commands share one product budget which must terminate,
	// collect target diagnostics, and leave persistence headroom before the
	// visible managed-provision acceptance deadline. The same execution budget
	// is sent to the agent, so a timed-out command cannot keep mutating after the
	// control-plane job has failed.
	managedStackKitInitTimeout     = 30 * time.Second
	managedStackKitGenerateTimeout = 60 * time.Second
	managedStackKitPlanTimeout     = 45 * time.Second
	managedStackKitApplyTimeout    = 240 * time.Second
	managedStackKitResultGrace     = 10 * time.Second
	managedStackKitDispatchMargin  = 5 * time.Second
	stackKitCommandResultGrace     = 30 * time.Second
	typedStackKitSequenceTimeout   = managedStackKitInitTimeout + managedStackKitGenerateTimeout + managedStackKitPlanTimeout + managedStackKitApplyTimeout + 4*managedStackKitResultGrace + managedStackKitDispatchMargin
)

func managedStackKitOperationTimeout(operation string) time.Duration {
	switch operation {
	case StackKitLifecycleInit, StackKitLifecycleGenerate,
		StackKitLifecyclePlan, StackKitLifecycleApply:
		// The sequence context is the single lifecycle deadline. Artificial
		// per-command caps made a healthy first-run init fail while it populated
		// its release cache, even though the rollout still had ample total time.
		return typedStackKitSequenceTimeout
	default:
		return 0
	}
}

// StackKitLifecycleRequest is the closed operator-facing lifecycle input.
// It deliberately has no argv, environment, binary path, or shell command.
type StackKitLifecycleRequest struct {
	StackID          string
	TenantID         string
	OwnerID          string
	AgentID          string
	Operation        string
	TargetRelease    string
	DryRun           bool
	Offline          bool
	OwnerApproved    bool
	WorkingDirectory string
	// SpecPath is a relative canonical StackSpec path inside WorkingDirectory.
	// Operator lifecycle calls default to stack-spec.yaml; deploy binds this to
	// the v2 document already materialized by the pinned generator.
	SpecPath         string
	StackName        string
	Domain           string
	ExpectedSpecHash string
	// StackKit selects the local execution binding. Apply refuses to guess it:
	// "Apply never infers that a planned target is this machine: the owner
	// names the exact Site, node, and channel this process owns, and anything
	// else stays unadmitted" (StackKits cmd/stackkit/commands/apply.go:71).
	StackKit      string
	InventoryJSON []byte
	ServiceKey    string
	LogTail       int32
	LogCursor     string
	ServiceID     string
	// DurableJobID and ServiceActionDigest contain only derived, non-secret
	// idempotency material. The raw HTTP key is never persisted.
	DurableJobID        string
	ServiceActionDigest string
}

// localExecutionBinding is the Site/node/channel triple one kit's owner runs.
//
// Techstack sent the basement triple for every kit, so a cloud-kit rollout
// named a binding its own plan does not contain and StackKits rejected it. The
// values mirror each kit's authoring.standaloneOwner in the StackKits CUE
// authority -- basement-kit/stackfile.cue:212-214 and
// cloud-kit/stackfile.cue:200-204 -- and the site and node are cross-checked
// against the resolved plan at dispatch, so drift surfaces instead of applying
// silently. An unknown kit fails closed rather than inheriting basement.
type localExecutionBinding struct {
	SiteRef             string
	NodeRef             string
	ExecutionChannelRef string
}

var localExecutionBindings = map[string]localExecutionBinding{
	"basement-kit": {SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"},
	"cloud-kit":    {SiteRef: "cloud", NodeRef: "cloud-main", ExecutionChannelRef: "host-channel-cloud-main"},
}

func localExecutionBindingFor(stackKit string) (localExecutionBinding, error) {
	binding, ok := localExecutionBindings[strings.ToLower(strings.TrimSpace(stackKit))]
	if !ok {
		return localExecutionBinding{}, fmt.Errorf(
			"no local execution binding is known for StackKit %q; apply refuses an unnamed Site, node, and channel",
			stackKit,
		)
	}
	return binding, nil
}

// StackKitCommandSender is implemented by grpcserver.Server. Keeping the job
// handler on this narrow seam prevents the operator route from reaching the
// generic agent command queue.
type StackKitCommandSender interface {
	SendStackKitCommand(context.Context, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

type tenantStackKitCommandSender interface {
	SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

type StackKitLifecycleConfig struct {
	Sender          StackKitCommandSender
	releaseResolver func() (*stackkitrelease.Release, error)
}

func NormalizeStackKitLifecycleRequest(req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	req.StackID = strings.TrimSpace(req.StackID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	req.TargetRelease = strings.TrimSpace(req.TargetRelease)
	req.WorkingDirectory = strings.TrimSpace(req.WorkingDirectory)
	req.SpecPath = strings.TrimSpace(req.SpecPath)
	req.StackName = strings.TrimSpace(req.StackName)
	req.Domain = strings.TrimSpace(req.Domain)
	req.ExpectedSpecHash = strings.TrimSpace(req.ExpectedSpecHash)
	req.ServiceKey = strings.TrimSpace(req.ServiceKey)
	req.LogCursor = strings.TrimSpace(req.LogCursor)
	req.ServiceID = strings.TrimSpace(req.ServiceID)
	req.DurableJobID = strings.TrimSpace(req.DurableJobID)
	req.ServiceActionDigest = strings.TrimSpace(req.ServiceActionDigest)
	if req.WorkingDirectory == "" {
		req.WorkingDirectory = defaultStackKitNodeWorkspace
	}
	if req.SpecPath == "" {
		req.SpecPath = "stack-spec.yaml"
	}
	if req.StackID == "" || req.TenantID == "" || req.OwnerID == "" || req.AgentID == "" {
		return req, fmt.Errorf("stack, tenant, Owner, and agent are required")
	}
	switch req.Operation {
	case StackKitLifecycleGenerate, StackKitLifecyclePlan, StackKitLifecycleVerify, StackKitLifecycleDriftDetect:
	case StackKitLifecycleInit, StackKitLifecycleApply, StackKitLifecycleDriftReconcile:
		if req.Operation == StackKitLifecycleInit && (req.StackKit == "" || req.StackName == "") {
			return req, fmt.Errorf("init requires StackKit and stack name")
		}
	case StackKitLifecycleUpgrade:
		if !req.DryRun && !req.OwnerApproved {
			return req, fmt.Errorf("upgrade requires explicit Owner approval")
		}
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop, StackKitLifecycleServiceRestart:
		if !req.OwnerApproved {
			return req, fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
		if req.ServiceKey == "" {
			return req, fmt.Errorf("%s requires service key", req.Operation)
		}
	case StackKitLifecycleServiceLogs:
		if req.ServiceKey == "" {
			return req, fmt.Errorf("service_logs requires service key")
		}
		if req.LogTail == 0 {
			req.LogTail = 100
		}
		if req.LogTail < 1 || req.LogTail > 200 {
			return req, fmt.Errorf("service_logs tail must be between 1 and 200")
		}
	default:
		return req, fmt.Errorf("unsupported StackKit lifecycle operation %q", req.Operation)
	}
	if req.DurableJobID != "" || req.ServiceActionDigest != "" || req.ServiceID != "" {
		if req.DurableJobID == "" || req.ServiceActionDigest == "" || req.ServiceID == "" || len(req.ServiceActionDigest) != sha256.Size*2 {
			return req, fmt.Errorf("durable service action requires job ID, service ID, and SHA-256 request digest")
		}
	}
	return req, nil
}

func StackKitLifecyclePayload(req StackKitLifecycleRequest) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":             req.TenantID,
		"owner_id":              req.OwnerID,
		"agent_id":              req.AgentID,
		"operation":             req.Operation,
		"target_release":        req.TargetRelease,
		"dry_run":               req.DryRun,
		"offline":               req.Offline,
		"owner_approved":        req.OwnerApproved,
		"working_directory":     req.WorkingDirectory,
		"spec_path":             req.SpecPath,
		"stackkit":              req.StackKit,
		"stack_name":            req.StackName,
		"domain":                req.Domain,
		"expected_spec_hash":    req.ExpectedSpecHash,
		"service_key":           req.ServiceKey,
		"log_tail":              req.LogTail,
		"log_cursor":            req.LogCursor,
		"service_id":            req.ServiceID,
		"durable_job_id":        req.DurableJobID,
		"service_action_digest": req.ServiceActionDigest,
	}
}

// StackKitServiceActionJobID derives a tenant/owner-scoped durable identity
// without storing the caller's raw Idempotency-Key.
func StackKitServiceActionJobID(tenantID, ownerID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(tenantID), strings.TrimSpace(ownerID), strings.TrimSpace(idempotencyKey),
	}, "\x00")))
	return fmt.Sprintf("job-stackkit-service-%x", digest[:16])
}

func StackKitServiceActionReceipt(req StackKitLifecycleRequest) map[string]interface{} {
	if strings.TrimSpace(req.ServiceActionDigest) == "" || strings.TrimSpace(req.ServiceID) == "" {
		return nil
	}
	return map[string]interface{}{
		"schema_version": "techstack.service-action-receipt/v1",
		"request_digest": req.ServiceActionDigest,
		"service_id":     req.ServiceID,
		"service_key":    req.ServiceKey,
		"action":         strings.TrimPrefix(req.Operation, "service_"),
		"log_tail":       req.LogTail,
		"log_cursor":     req.LogCursor,
	}
}

func MatchesStackKitServiceActionReceipt(result map[string]any, req StackKitLifecycleRequest) bool {
	receipt, ok := result["service_action_receipt"].(map[string]any)
	if !ok {
		return false
	}
	want := StackKitServiceActionReceipt(req)
	return stringFromInterface(receipt["schema_version"]) == stringFromInterface(want["schema_version"]) &&
		stringFromInterface(receipt["request_digest"]) == req.ServiceActionDigest &&
		stringFromInterface(receipt["service_id"]) == req.ServiceID &&
		stringFromInterface(receipt["service_key"]) == req.ServiceKey &&
		stringFromInterface(receipt["action"]) == strings.TrimPrefix(req.Operation, "service_")
}

func RegisterStackKitLifecycleHandler(q *Queue, cfg StackKitLifecycleConfig) {
	q.RegisterHandler(JobTypeStackKitLifecycle, StackKitLifecycleHandler(cfg))
}

func StackKitLifecycleHandler(cfg StackKitLifecycleConfig) JobHandler {
	return func(ctx context.Context, job *Job, q *Queue) error {
		if cfg.Sender == nil {
			return NewConfigError(fmt.Errorf("typed StackKits dispatcher is not configured"))
		}
		req, err := stackKitLifecycleRequestFromJob(job)
		if err != nil {
			return NewPermanentError(err)
		}
		resolveRelease := cfg.releaseResolver
		if resolveRelease == nil {
			resolveRelease = configuredTargetStackKitRelease
		}
		release, err := resolveRelease()
		if err != nil {
			return NewConfigError(err)
		}
		if release == nil {
			return NewConfigError(fmt.Errorf("pinned published StackKits release is not configured"))
		}

		job.setStep("stackkit_dispatch")
		q.UpdateProgress(job.ID, 10, "Dispatching typed StackKits "+req.Operation+" operation")
		expectedPlanHash := ""
		if req.Operation == StackKitLifecycleApply {
			job.setStep("stackkit_plan_admission")
			q.UpdateProgress(job.ID, 10, "Planning the exact StackKits generation before Apply")
			expectedPlanHash, err = planStackKitLifecycleApply(ctx, cfg.Sender, job.ID, req, *release)
			if err != nil {
				return NewPermanentError(err)
			}
			job.setStep("stackkit_dispatch")
			q.UpdateProgress(job.ID, 30, "Applying the admitted StackKits plan")
		}
		command, err := stackKitLifecycleCommand(job.ID, req, *release)
		if err != nil {
			return NewPermanentError(err)
		}
		command.ExpectedPlanHash = expectedPlanHash
		result, err := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, command)
		if err != nil {
			return NewResourceError(fmt.Errorf("typed StackKits dispatch failed: %w", err))
		}

		job.setStep("stackkit_result")
		normalized, err := normalizeStackKitLifecycleResult(req, result)
		if err != nil {
			return NewPermanentError(err)
		}
		if !result.Success {
			job.replaceResult(normalized)
			return NewPermanentError(fmt.Errorf("StackKits %s failed with exit code %d", req.Operation, result.ExitCode))
		}
		if isStackKitServiceMutation(req.Operation) {
			verifyRequest := req
			verifyRequest.Operation = StackKitLifecycleVerify
			verifyRequest.OwnerApproved = false
			verifyCommand, verifyErr := stackKitLifecycleCommand(job.ID+"-verify", verifyRequest, *release)
			if verifyErr != nil {
				return NewPermanentError(fmt.Errorf("build post-service verify: %w", verifyErr))
			}
			job.setStep("stackkit_service_verify")
			q.UpdateProgress(job.ID, 70, "Verifying StackKits service state")
			verifyResult, verifyErr := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, verifyCommand)
			if verifyErr != nil {
				return NewResourceError(fmt.Errorf("post-service StackKits verify dispatch failed: %w", verifyErr))
			}
			verifyNormalized, verifyErr := normalizeStackKitLifecycleResult(verifyRequest, verifyResult)
			if verifyErr != nil {
				return NewPermanentError(fmt.Errorf("normalize post-service StackKits verify: %w", verifyErr))
			}
			normalized["verification"] = verifyNormalized
			if !verifyResult.Success {
				job.replaceResult(normalized)
				return NewPermanentError(fmt.Errorf("post-service StackKits verify failed with exit code %d", verifyResult.ExitCode))
			}
		}
		job.replaceResult(normalized)
		q.UpdateProgress(job.ID, 100, "StackKits "+req.Operation+" completed")
		return nil
	}
}

func planStackKitLifecycleApply(ctx context.Context, sender StackKitCommandSender, jobID string, req StackKitLifecycleRequest, release stackkitrelease.Release) (string, error) {
	planRequest := req
	planRequest.Operation = StackKitLifecyclePlan
	planRequest.OwnerApproved = false
	command, err := stackKitLifecycleCommand(jobID+"-plan", planRequest, release)
	if err != nil {
		return "", fmt.Errorf("build typed StackKits plan admission: %w", err)
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, sender, req.TenantID, req.AgentID, command)
	if err != nil {
		return "", fmt.Errorf("typed StackKits plan admission dispatch failed: %w", err)
	}
	if result == nil || !result.Success {
		return "", fmt.Errorf("typed StackKits plan admission failed")
	}
	planHash, err := typedStackKitPlanHash(result)
	if err != nil {
		return "", fmt.Errorf("admit typed StackKits plan: %w", err)
	}
	return planHash, nil
}

func isStackKitServiceMutation(operation string) bool {
	switch operation {
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop, StackKitLifecycleServiceRestart:
		return true
	default:
		return false
	}
}

func stackKitLifecycleRequestFromJob(job *Job) (StackKitLifecycleRequest, error) {
	if job == nil {
		return StackKitLifecycleRequest{}, fmt.Errorf("StackKits lifecycle job is required")
	}
	req := StackKitLifecycleRequest{
		StackID:             job.TargetID,
		TenantID:            stringFromInterface(job.Payload["tenant_id"]),
		OwnerID:             stringFromInterface(job.Payload["owner_id"]),
		AgentID:             stringFromInterface(job.Payload["agent_id"]),
		Operation:           stringFromInterface(job.Payload["operation"]),
		TargetRelease:       stringFromInterface(job.Payload["target_release"]),
		DryRun:              boolFromInterface(job.Payload["dry_run"]),
		Offline:             boolFromInterface(job.Payload["offline"]),
		OwnerApproved:       boolFromInterface(job.Payload["owner_approved"]),
		WorkingDirectory:    stringFromInterface(job.Payload["working_directory"]),
		SpecPath:            stringFromInterface(job.Payload["spec_path"]),
		StackName:           stringFromInterface(job.Payload["stack_name"]),
		Domain:              stringFromInterface(job.Payload["domain"]),
		ExpectedSpecHash:    stringFromInterface(job.Payload["expected_spec_hash"]),
		ServiceKey:          stringFromInterface(job.Payload["service_key"]),
		LogTail:             int32FromInterface(job.Payload["log_tail"]),
		LogCursor:           stringFromInterface(job.Payload["log_cursor"]),
		ServiceID:           stringFromInterface(job.Payload["service_id"]),
		DurableJobID:        stringFromInterface(job.Payload["durable_job_id"]),
		ServiceActionDigest: stringFromInterface(job.Payload["service_action_digest"]),
		StackKit: firstNonEmpty(
			stringFromInterface(job.Payload["stackkit"]),
			stringFromInterface(job.Payload["stackkit_catalog_ref"]),
			stringFromInterface(job.Payload["catalog_ref"]),
		),
	}
	return NormalizeStackKitLifecycleRequest(req)
}

func stackKitLifecycleCommand(commandID string, req StackKitLifecycleRequest, release stackkitrelease.Release) (*agentpb.StackKitCommand, error) {
	operation, err := stackKitLifecycleAgentOperation(req.Operation)
	if err != nil {
		return nil, err
	}
	binding, err := localExecutionBindingFor(req.StackKit)
	if err != nil {
		return nil, err
	}
	command := &agentpb.StackKitCommand{
		CommandId:                commandID,
		Operation:                operation,
		WorkingDirectory:         req.WorkingDirectory,
		SpecPath:                 req.SpecPath,
		OutputDirectory:          "deploy",
		TimeoutSeconds:           stackKitLifecycleTimeout(operation),
		Release:                  grpcserver.StackKitReleasePinFor(release),
		Offline:                  req.Offline,
		DryRun:                   req.DryRun,
		TargetRelease:            req.TargetRelease,
		OwnerApproved:            req.OwnerApproved,
		LocalSiteRef:             binding.SiteRef,
		LocalNodeRef:             binding.NodeRef,
		LocalExecutionChannelRef: binding.ExecutionChannelRef,
		Stackkit:                 req.StackKit,
		StackName:                req.StackName,
		Domain:                   req.Domain,
		ExpectedSpecHash:         req.ExpectedSpecHash,
		InventoryJson:            append([]byte(nil), req.InventoryJSON...),
		ServiceKey:               req.ServiceKey,
		LogTail:                  req.LogTail,
		LogCursor:                req.LogCursor,
	}
	if operation == agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE {
		command.DriftMode = agentpb.StackKitDriftMode_STACKKIT_DRIFT_MODE_STANDARD
	}
	return command, nil
}

func stackKitLifecycleAgentOperation(operation string) (agentpb.StackKitOperation, error) {
	switch operation {
	case StackKitLifecycleInit:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_INIT, nil
	case StackKitLifecycleGenerate:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE, nil
	case StackKitLifecyclePlan:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN, nil
	case StackKitLifecycleApply:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY, nil
	case StackKitLifecycleVerify:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, nil
	case StackKitLifecycleUpgrade:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE, nil
	case StackKitLifecycleDriftDetect:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT, nil
	case StackKitLifecycleDriftReconcile:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE, nil
	case StackKitLifecycleServiceStart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START, nil
	case StackKitLifecycleServiceStop:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP, nil
	case StackKitLifecycleServiceRestart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART, nil
	case StackKitLifecycleServiceLogs:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS, nil
	default:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UNSPECIFIED, fmt.Errorf("unsupported StackKits lifecycle operation %q", operation)
	}
}

func stackKitLifecycleTimeout(operation agentpb.StackKitOperation) int32 {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_RECONCILE:
		return int32(stackKitWriteCommandTimeout / time.Second)
	default:
		return int32(stackKitReadCommandTimeout / time.Second)
	}
}

func sendStackKitCommandBounded(ctx context.Context, sender StackKitCommandSender, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	return sendStackKitCommandBoundedWithGraceForTenant(ctx, sender, "", agentID, command, stackKitCommandResultGrace)
}

func sendStackKitCommandBoundedWithGrace(ctx context.Context, sender StackKitCommandSender, agentID string, command *agentpb.StackKitCommand, resultGrace time.Duration) (*agentpb.StackKitResult, error) {
	return sendStackKitCommandBoundedWithGraceForTenant(ctx, sender, "", agentID, command, resultGrace)
}

func sendStackKitCommandBoundedForTenant(ctx context.Context, sender StackKitCommandSender, tenantID, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	return sendStackKitCommandBoundedWithGraceForTenant(ctx, sender, tenantID, agentID, command, stackKitCommandResultGrace)
}

func sendStackKitCommandBoundedWithGraceForTenant(ctx context.Context, sender StackKitCommandSender, tenantID, agentID string, command *agentpb.StackKitCommand, resultGrace time.Duration) (*agentpb.StackKitResult, error) {
	if sender == nil {
		return nil, fmt.Errorf("typed StackKits dispatcher is not configured")
	}
	if resultGrace <= 0 {
		resultGrace = stackKitCommandResultGrace
	}
	timeout := stackKitReadCommandTimeout + resultGrace
	if command != nil && command.TimeoutSeconds > 0 {
		timeout = time.Duration(command.TimeoutSeconds)*time.Second + resultGrace
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if scoped, ok := sender.(tenantStackKitCommandSender); ok {
		return scoped.SendStackKitCommandForTenant(commandCtx, tenantID, agentID, command)
	}
	return sender.SendStackKitCommand(commandCtx, agentID, command)
}

func normalizeStackKitLifecycleResult(req StackKitLifecycleRequest, result *agentpb.StackKitResult) (map[string]interface{}, error) {
	if result == nil {
		return nil, fmt.Errorf("typed StackKits result is missing")
	}
	var commandResult map[string]interface{}
	if err := json.Unmarshal(result.CommandResultJson, &commandResult); err != nil {
		return nil, fmt.Errorf("decode StackKits command result: %w", err)
	}
	events := make([]interface{}, 0, len(result.EventsJsonl))
	for _, raw := range result.EventsJsonl {
		var event map[string]interface{}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, fmt.Errorf("decode StackKits rollout event: %w", err)
		}
		events = append(events, event)
	}
	status := stackKitLifecycleCommandDataStatus(commandResult)
	normalized := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1",
		"operation":      req.Operation,
		"agent_id":       req.AgentID,
		"success":        result.Success,
		"exit_code":      result.ExitCode,
		"status":         status,
		"command_result": commandResult,
		"events":         events,
		"stderr":         result.Stderr,
		"release": map[string]interface{}{
			"version":              result.Release.GetVersion(),
			"platform_os":          result.Release.GetPlatformOs(),
			"platform_arch":        result.Release.GetPlatformArch(),
			"archive_sha256":       result.Release.GetArchiveSha256(),
			"release_index_sha256": result.Release.GetReleaseIndexSha256(),
		},
	}
	if receipt := StackKitServiceActionReceipt(req); receipt != nil {
		normalized["service_action_receipt"] = receipt
	}
	if req.Operation == StackKitLifecycleServiceLogs {
		data, _ := commandResult["data"].(map[string]interface{})
		if output := strings.TrimSpace(stringFromInterface(data["output"])); output != "" {
			var page map[string]interface{}
			if err := json.Unmarshal([]byte(output), &page); err != nil {
				return nil, fmt.Errorf("decode StackKits service log page: %w", err)
			}
			normalized["service_logs"] = page
		}
	}
	return normalized, nil
}

func stackKitLifecycleCommandDataStatus(commandResult map[string]interface{}) string {
	data, ok := commandResult["data"].(map[string]interface{})
	if !ok {
		return ""
	}
	status, _ := data["status"].(string)
	return strings.ToLower(strings.TrimSpace(status))
}
