package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/workloadremoval"
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
	StackKitLifecycleRemove         = "remove"
	StackKitLifecycleAddressBind    = "address_bind"
	// StackKitLifecycleAdvancedTrustImport installs this installation's
	// Advanced issuer trust bundle on the managed host. Every managed rollout
	// runs it after init; operators may re-run it to refresh the trust.
	StackKitLifecycleAdvancedTrustImport = "advanced_trust_import"
	// The native backup operations reuse the typed BACKUP_* commands the
	// managed rollout's restore drill runs. Each binds the node-local plan it
	// acts on; restore only stages a snapshot into StackKits' isolated area.
	StackKitLifecycleBackupRun       = "backup_run"
	StackKitLifecycleBackupStatus    = "backup_status"
	StackKitLifecycleBackupConfigure = "backup_configure"
	StackKitLifecycleBackupRestore   = "backup_restore"
	// Advanced Mode operator operations (ADR-0045). Each dispatches
	// capability-gated StackKits Advanced operations from the pinned
	// release's catalog: advanced_change_set creates and applies a change set
	// for the candidate StackSpec in the request body, drift_reconcile
	// reconciles through a change set, rollback runs the coordinated rollback
	// to rollback_target_ref, and restore_drill runs the native restore drill.
	StackKitLifecycleAdvancedChangeSet = "advanced_change_set"
	StackKitLifecycleRollback          = "rollback"
	StackKitLifecycleRestoreDrill      = "restore_drill"

	defaultStackKitNodeWorkspace = "/opt/stackkit"
	stackKitReadCommandTimeout   = 5 * time.Minute
	stackKitWriteCommandTimeout  = 12 * time.Minute
	stackKitBackupCommandTimeout = 15 * time.Minute
	// StackKits bounds the Advanced target at 15 minutes and gives a failed
	// target a separate 10-minute recovery context. Admission and checkpoint
	// also run inside this command, so the agent must outlive both phases.
	stackKitAdvancedMutationCommandTimeout = 30 * time.Minute

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
	case StackKitLifecycleInit, StackKitLifecycleAdvancedTrustImport, StackKitLifecycleAddressBind,
		StackKitLifecycleGenerate, StackKitLifecyclePlan, StackKitLifecycleApply:
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
	BackupRenewal      *advancedissuer.BackupRenewal
	StackID            string
	StackKitInstanceID string
	TenantID           string
	OwnerID            string
	AgentID            string
	NodeID             string
	Operation          string
	TargetRelease      string
	DryRun             bool
	Offline            bool
	OwnerApproved      bool
	WorkingDirectory   string
	// SpecPath is a relative canonical StackSpec path inside WorkingDirectory.
	// Deploy binds this to the v2 document already materialized by the pinned
	// generator; operator lifecycle calls default to the StackSpec the stack's
	// last managed rollout applied (ApplyStackKitRolloutDefaults).
	SpecPath          string
	StackName         string
	Domain            string
	ExpectedSpecHash  string
	CandidateSpecJSON []byte
	// OwnerEmail is the stack Owner's signed-in account email, sent with init
	// only. StackKits requires it for a fresh local Owner (the PocketID owner).
	OwnerEmail string
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
	WorkloadRef         string
	AddressPrefix       string
	BoundSpecPath       string
	// SnapshotAnchorID names the snapshot a staged backup restore reads.
	SnapshotAnchorID string
	// AdvancedTrustBundle is the installation's canonical public
	// stackkit.advanced-trust-bundle/v1, set at dispatch for
	// advanced_trust_import only and never persisted in a job payload.
	AdvancedTrustBundle []byte
	// RollbackTargetRef is the sha256 executor-state snapshot id or change-set
	// id a rollback returns to.
	RollbackTargetRef string
}

// AdvancedIssuer is the installation's Advanced capability issuer as seen by
// lifecycle dispatch: the public trust bundle every managed host imports, and
// the record of which local Owner each host bound it to.
type AdvancedIssuer interface {
	TrustBundle() []byte
	RecordTrustBinding(context.Context, advancedissuer.TrustBinding) error
	// TrustBindingFor returns the Owner and stack a deployment's host bound
	// the trust to; Issue mints one capability for one Advanced operation.
	TrustBindingFor(ctx context.Context, tenantID, deploymentID string) (advancedissuer.TrustBinding, error)
	Issue(context.Context, advancedissuer.Request) (advancedissuer.Capability, error)
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

// stackKitRestoreOnlyCommander lets a least-privilege sender declare that it
// owns only the native backup recovery sequence, including its binding plan.
// Ordinary production commanders omit this marker and continue to own the
// full typed lifecycle.
type stackKitRestoreOnlyCommander interface {
	StackKitCommandSender
	StackKitRestoreOnly() bool
}

func stackKitCommanderOwnsFullLifecycle(sender StackKitCommandSender) bool {
	scoped, ok := sender.(stackKitRestoreOnlyCommander)
	return sender != nil && (!ok || !scoped.StackKitRestoreOnly())
}

type tenantStackKitCommandSender interface {
	SendStackKitCommandForTenant(context.Context, string, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error)
}

type StackKitLifecycleConfig struct {
	ManagedStackKitInventory ManagedStackKitInventoryBuilder
	ManagedAddressAuthority  ManagedAddressAuthority
	Sender                   StackKitCommandSender
	AdvancedIssuer           AdvancedIssuer
	releaseResolver          func() (*stackkitrelease.Release, error)
}

func NormalizeStackKitLifecycleRequest(req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	req = normalizeStackKitLifecycleFields(req)
	if req.WorkingDirectory == "" {
		req.WorkingDirectory = defaultStackKitNodeWorkspace
	}
	if req.StackID == "" || req.TenantID == "" || req.OwnerID == "" || req.AgentID == "" {
		return req, fmt.Errorf("stack, tenant, Owner, and agent are required")
	}
	if err := validateStackKitLifecycleOperation(&req); err != nil {
		return req, err
	}
	if err := validateDurableStackKitServiceAction(req); err != nil {
		return req, err
	}
	return req, nil
}

func normalizeStackKitLifecycleFields(req StackKitLifecycleRequest) StackKitLifecycleRequest {
	req.StackID = strings.TrimSpace(req.StackID)
	req.StackKitInstanceID = strings.TrimSpace(req.StackKitInstanceID)
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.NodeID = strings.TrimSpace(req.NodeID)
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
	req.WorkloadRef = strings.TrimSpace(req.WorkloadRef)
	req.AddressPrefix = strings.TrimSpace(req.AddressPrefix)
	req.BoundSpecPath = strings.TrimSpace(req.BoundSpecPath)
	req.SnapshotAnchorID = strings.TrimSpace(req.SnapshotAnchorID)
	req.RollbackTargetRef = strings.TrimSpace(req.RollbackTargetRef)
	return req
}

func validateStackKitLifecycleOperation(req *StackKitLifecycleRequest) error {
	switch req.Operation {
	case StackKitLifecycleGenerate, StackKitLifecyclePlan, StackKitLifecycleVerify, StackKitLifecycleDriftDetect:
	case StackKitLifecycleAddressBind:
		if req.AddressPrefix == "" || req.BoundSpecPath == "" {
			return fmt.Errorf("address bind requires prefix and bound StackSpec path")
		}
	case StackKitLifecycleDriftReconcile, StackKitLifecycleAdvancedChangeSet:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
		if req.Operation == StackKitLifecycleAdvancedChangeSet && len(req.CandidateSpecJSON) == 0 {
			return fmt.Errorf("advanced_change_set requires the candidate StackSpec")
		}
		if len(req.CandidateSpecJSON) > stackkitcommand.MaxInitCandidateBytes || (len(req.CandidateSpecJSON) > 0 && !json.Valid(req.CandidateSpecJSON)) {
			return fmt.Errorf("%s candidate must be one JSON StackSpec within %d bytes", req.Operation, stackkitcommand.MaxInitCandidateBytes)
		}
	case StackKitLifecycleRollback:
		if !req.OwnerApproved {
			return fmt.Errorf("rollback requires explicit Owner approval")
		}
		if !stackKitSnapshotAnchorPattern.MatchString(req.RollbackTargetRef) {
			return fmt.Errorf("rollback requires a sha256 rollback_target_ref (snapshot or change-set id)")
		}
	case StackKitLifecycleRestoreDrill:
		if !req.OwnerApproved {
			return fmt.Errorf("restore_drill requires explicit Owner approval")
		}
		if req.SnapshotAnchorID != "" && !stackKitSnapshotAnchorPattern.MatchString(req.SnapshotAnchorID) {
			return fmt.Errorf("restore_drill snapshot_anchor_id must be a sha256 anchor")
		}
	case StackKitLifecycleInit, StackKitLifecycleApply:
		if req.Operation == StackKitLifecycleInit && (req.StackKit == "" || req.StackName == "") {
			return fmt.Errorf("init requires StackKit and stack name")
		}
		if req.Operation == StackKitLifecycleInit {
			return stackkitcommand.ValidateInitCandidate(req.CandidateSpecJSON, req.StackKit, req.StackName)
		}
	case StackKitLifecycleAdvancedTrustImport:
		if !req.OwnerApproved {
			return fmt.Errorf("advanced_trust_import requires explicit Owner approval")
		}
	case StackKitLifecycleUpgrade:
		if !req.DryRun && !req.OwnerApproved {
			return fmt.Errorf("upgrade requires explicit Owner approval")
		}
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop, StackKitLifecycleServiceRestart:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
		if req.ServiceKey == "" {
			return fmt.Errorf("%s requires service key", req.Operation)
		}
	case StackKitLifecycleServiceLogs:
		if req.ServiceKey == "" {
			return fmt.Errorf("service_logs requires service key")
		}
		if req.LogTail == 0 {
			req.LogTail = 100
		}
		if req.LogTail < 1 || req.LogTail > 200 {
			return fmt.Errorf("service_logs tail must be between 1 and 200")
		}
	case StackKitLifecycleBackupStatus:
	case StackKitLifecycleBackupRun, StackKitLifecycleBackupConfigure:
		if !req.OwnerApproved {
			return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
		}
	case StackKitLifecycleBackupRestore:
		if !req.OwnerApproved {
			return fmt.Errorf("backup_restore requires explicit Owner approval")
		}
		if !stackKitSnapshotAnchorPattern.MatchString(req.SnapshotAnchorID) {
			return fmt.Errorf("backup_restore requires a sha256 snapshot_anchor_id")
		}
	case StackKitLifecycleRemove:
		if !req.OwnerApproved {
			return fmt.Errorf("remove requires explicit Owner approval")
		}
		if req.WorkloadRef == "" || req.WorkloadRef != strings.ToLower(req.WorkloadRef) {
			return fmt.Errorf("remove requires one canonical workload ref")
		}
	default:
		return fmt.Errorf("unsupported StackKit lifecycle operation %q", req.Operation)
	}
	return nil
}

func validateDurableStackKitServiceAction(req StackKitLifecycleRequest) error {
	if req.DurableJobID != "" || req.ServiceActionDigest != "" || req.ServiceID != "" {
		if req.DurableJobID == "" || req.ServiceActionDigest == "" || req.ServiceID == "" || len(req.ServiceActionDigest) != sha256.Size*2 {
			return fmt.Errorf("durable service action requires job ID, service ID, and SHA-256 request digest")
		}
	}
	return nil
}

func StackKitLifecyclePayload(req StackKitLifecycleRequest) map[string]interface{} {
	return map[string]interface{}{
		"stackkit_instance_id":  req.StackKitInstanceID,
		"tenant_id":             req.TenantID,
		"owner_id":              req.OwnerID,
		"agent_id":              req.AgentID,
		"node_id":               req.NodeID,
		"operation":             req.Operation,
		"target_release":        req.TargetRelease,
		"dry_run":               req.DryRun,
		"offline":               req.Offline,
		"owner_approved":        req.OwnerApproved,
		"working_directory":     req.WorkingDirectory,
		"spec_path":             req.SpecPath,
		"stackkit":              req.StackKit,
		"stackkit_id":           req.StackKit,
		"stack_name":            req.StackName,
		"domain":                req.Domain,
		"expected_spec_hash":    req.ExpectedSpecHash,
		"candidate_spec_json":   string(req.CandidateSpecJSON),
		"service_key":           req.ServiceKey,
		"log_tail":              req.LogTail,
		"log_cursor":            req.LogCursor,
		"service_id":            req.ServiceID,
		"durable_job_id":        req.DurableJobID,
		"service_action_digest": req.ServiceActionDigest,
		"workload_ref":          req.WorkloadRef,
		"snapshot_anchor_id":    req.SnapshotAnchorID,
		"rollback_target_ref":   req.RollbackTargetRef,
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
		"schema_version":       "techstack.service-action-receipt/v1",
		"techstack_id":         req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID,
		"request_digest":       req.ServiceActionDigest,
		"service_id":           req.ServiceID,
		"service_key":          req.ServiceKey,
		"action":               strings.TrimPrefix(req.Operation, "service_"),
		"log_tail":             req.LogTail,
		"log_cursor":           req.LogCursor,
	}
}

func MatchesStackKitServiceActionReceipt(result map[string]any, req StackKitLifecycleRequest) bool {
	receipt, ok := result["service_action_receipt"].(map[string]any)
	if !ok {
		return false
	}
	want := StackKitServiceActionReceipt(req)
	if actual := strings.TrimSpace(stringFromInterface(receipt["techstack_id"])); actual != "" && actual != req.StackID {
		return false
	}
	if actual := strings.TrimSpace(stringFromInterface(receipt["stackkit_instance_id"])); actual != "" && actual != req.StackKitInstanceID {
		return false
	}
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
		if IsAdvancedStackKitLifecycleOperation(req.Operation) {
			return runAdvancedStackKitLifecycleJob(ctx, cfg, job, q, req, *release)
		}
		if req.Operation == StackKitLifecycleAdvancedTrustImport {
			if cfg.AdvancedIssuer == nil {
				return NewConfigError(advancedissuer.ErrUnavailable)
			}
			req.AdvancedTrustBundle = cfg.AdvancedIssuer.TrustBundle()
		}
		expectedPlanHash := ""
		if req.Operation == StackKitLifecycleApply || IsStackKitBackupOperation(req.Operation) {
			// Apply and every native backup operation act on one exact
			// node-local generation; plan it first and bind the command to it.
			job.setStep("stackkit_plan_admission")
			q.UpdateProgress(job.ID, 10, "Planning the exact StackKits generation before "+req.Operation)
			expectedPlanHash, err = planStackKitLifecycleApply(ctx, cfg.Sender, job.ID, req, *release)
			if err != nil {
				return NewPermanentError(err)
			}
			job.setStep("stackkit_dispatch")
			q.UpdateProgress(job.ID, 30, "Dispatching "+req.Operation+" against the admitted StackKits plan")
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
		if req.Operation == StackKitLifecycleAdvancedTrustImport {
			binding, bindErr := admitAdvancedTrustImport(ctx, cfg.AdvancedIssuer, req, req.StackKitInstanceID, command, result)
			if bindErr != nil {
				job.replaceResult(normalized)
				return NewPermanentError(bindErr)
			}
			normalized["advanced_trust"] = binding
		}
		if evidence, evidenceErr := stackKitBackupEvidence(command, result); evidenceErr != nil {
			job.replaceResult(normalized)
			return NewPermanentError(evidenceErr)
		} else if evidence != nil {
			normalized["backup"] = evidence
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
		StackKitInstanceID:  stringFromInterface(job.Payload["stackkit_instance_id"]),
		TenantID:            stringFromInterface(job.Payload["tenant_id"]),
		OwnerID:             stringFromInterface(job.Payload["owner_id"]),
		AgentID:             stringFromInterface(job.Payload["agent_id"]),
		NodeID:              stringFromInterface(job.Payload["node_id"]),
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
		CandidateSpecJSON:   []byte(stringFromInterface(job.Payload["candidate_spec_json"])),
		ServiceKey:          stringFromInterface(job.Payload["service_key"]),
		LogTail:             int32FromInterface(job.Payload["log_tail"]),
		LogCursor:           stringFromInterface(job.Payload["log_cursor"]),
		ServiceID:           stringFromInterface(job.Payload["service_id"]),
		DurableJobID:        stringFromInterface(job.Payload["durable_job_id"]),
		ServiceActionDigest: stringFromInterface(job.Payload["service_action_digest"]),
		WorkloadRef:         stringFromInterface(job.Payload["workload_ref"]),
		SnapshotAnchorID:    stringFromInterface(job.Payload["snapshot_anchor_id"]),
		RollbackTargetRef:   stringFromInterface(job.Payload["rollback_target_ref"]),
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
	specPath := req.SpecPath
	if specPath == "" {
		specPath = defaultStackKitSpecPath
	}
	command := &agentpb.StackKitCommand{
		CommandId:                commandID,
		Operation:                operation,
		WorkingDirectory:         req.WorkingDirectory,
		SpecPath:                 specPath,
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
		ServiceKey:               req.ServiceKey,
		LogTail:                  req.LogTail,
		LogCursor:                req.LogCursor,
		StackkitInstanceId:       req.StackKitInstanceID,
		WorkloadRef:              req.WorkloadRef,
		AddressPrefix:            req.AddressPrefix,
		BoundSpecPath:            req.BoundSpecPath,
	}
	if operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE {
		command.SnapshotAnchorId = req.SnapshotAnchorID
	}
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		command.CandidateSpecJson = append([]byte(nil), req.CandidateSpecJSON...)
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK:
		command.RollbackTargetRef = req.RollbackTargetRef
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		command.SnapshotAnchorId = req.SnapshotAnchorID
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_INIT:
		command.CandidateSpecJson = append([]byte(nil), req.CandidateSpecJSON...)
		command.OwnerEmail = strings.TrimSpace(req.OwnerEmail)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT:
		// Trust import reads only the bundle; it needs no Inventory.
		if len(req.AdvancedTrustBundle) == 0 {
			return nil, advancedissuer.ErrUnavailable
		}
		command.AdvancedTrustBundle = append([]byte(nil), req.AdvancedTrustBundle...)
	default:
		// Init admits desired intent without Inventory. Deliver observed facts
		// with the following operation, keeping the two bounded documents out
		// of the same transport frame.
		command.InventoryJson = append([]byte(nil), req.InventoryJSON...)
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
	case stackKitStepChangeSetCreate:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE, nil
	case stackKitStepChangeSetApply:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY, nil
	case stackKitStepDriftReconcile:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE, nil
	case stackKitStepRollback:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK, nil
	case stackKitStepRestoreDrill:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL, nil
	case StackKitLifecycleServiceStart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START, nil
	case StackKitLifecycleServiceStop:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP, nil
	case StackKitLifecycleServiceRestart:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART, nil
	case StackKitLifecycleServiceLogs:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS, nil
	case StackKitLifecycleRemove:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE, nil
	case StackKitLifecycleAddressBind:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND, nil
	case StackKitLifecycleAdvancedTrustImport:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT, nil
	case StackKitLifecycleBackupRun:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN, nil
	case StackKitLifecycleBackupStatus:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_STATUS, nil
	case StackKitLifecycleBackupConfigure:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE, nil
	case StackKitLifecycleBackupRestore:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE, nil
	default:
		return agentpb.StackKitOperation_STACKKIT_OPERATION_UNSPECIFIED, fmt.Errorf("unsupported StackKits lifecycle operation %q", operation)
	}
}

func stackKitLifecycleTimeout(operation agentpb.StackKitOperation) int32 {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_UPGRADE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_CREATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_REMOVE:
		return int32(stackKitWriteCommandTimeout / time.Second)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ROLLBACK,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		// The managed restore drill's per-step budget.
		return int32(stackKitBackupCommandTimeout / time.Second)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_CHANGE_SET_APPLY,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_DRIFT_RECONCILE:
		return int32(stackKitAdvancedMutationCommandTimeout / time.Second)
	default:
		return int32(stackKitReadCommandTimeout / time.Second)
	}
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
		"schema_version":       "techstack.stackkit-lifecycle-result/v1",
		"techstack_id":         req.StackID,
		"stackkit_instance_id": req.StackKitInstanceID,
		"operation":            req.Operation,
		"agent_id":             req.AgentID,
		"success":              result.Success,
		"exit_code":            result.ExitCode,
		"status":               status,
		"command_result":       commandResult,
		"events":               events,
		"stderr":               result.Stderr,
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
	if req.Operation == StackKitLifecycleDriftDetect && result.Success {
		report, err := stackkitcommand.ParseDriftReport(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_DRIFT_DETECT}, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits drift report: %w", err)
		}
		normalized["drift_report"] = driftReportMap(report)
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
	if req.Operation == StackKitLifecycleRemove && result.Success {
		data, _ := commandResult["data"].(map[string]interface{})
		raw := []byte(strings.TrimSpace(stringFromInterface(data["output"])))
		evidence, err := workloadremoval.ParseEvidence(raw)
		if err != nil {
			return nil, fmt.Errorf("validate StackKits workload-removal evidence: %w", err)
		}
		if evidence.Authority.WorkloadRef != req.WorkloadRef {
			return nil, fmt.Errorf("StackKits workload-removal evidence does not match dispatched workload authority")
		}
		var persisted map[string]interface{}
		if err := json.Unmarshal(raw, &persisted); err != nil {
			return nil, fmt.Errorf("persist StackKits workload-removal evidence: %w", err)
		}
		normalized["removal_evidence"] = persisted
	}
	return normalized, nil
}

// IsStackKitBackupOperation reports the native backup operations.
func IsStackKitBackupOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case StackKitLifecycleBackupRun, StackKitLifecycleBackupStatus,
		StackKitLifecycleBackupConfigure, StackKitLifecycleBackupRestore:
		return true
	default:
		return false
	}
}

var stackKitSnapshotAnchorPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// stackKitBackupEvidence admits the snapshot or staged-restore identity of a
// successful backup operation, so the caller can name the snapshot a later
// staged restore reads. Other operations carry no backup evidence.
func stackKitBackupEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (map[string]interface{}, error) {
	if command == nil || result == nil || !result.Success {
		return nil, nil
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN:
		evidence, err := stackkitcommand.ParseBackupRunEvidence(command, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits backup run evidence: %w", err)
		}
		return map[string]interface{}{
			"snapshot_anchor_id": evidence.SnapshotAnchorID, "operation_id": evidence.OperationID,
			"owner_ref": evidence.OwnerRef, "plan_hash": evidence.PlanHash,
		}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE:
		evidence, err := stackkitcommand.ParseBackupRestoreEvidence(command, result)
		if err != nil {
			return nil, fmt.Errorf("admit StackKits staged restore evidence: %w", err)
		}
		return map[string]interface{}{
			"mode": "staged", "restore_result_id": evidence.RestoreResultID,
			"snapshot_anchor_id": evidence.SnapshotAnchorID, "operation_id": evidence.OperationID,
			"owner_ref": evidence.OwnerRef, "plan_hash": evidence.PlanHash,
			"verified_at": evidence.VerifiedAt.UTC().Format(time.RFC3339Nano),
		}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_CONFIGURE:
		if err := stackkitcommand.ParseBackupConfigurationEvidence(command, result); err != nil {
			return nil, fmt.Errorf("admit StackKits backup configuration evidence: %w", err)
		}
		return map[string]interface{}{"status": "configured"}, nil
	default:
		return nil, nil
	}
}

func IsStackKitServiceLifecycleOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case StackKitLifecycleServiceStart, StackKitLifecycleServiceStop,
		StackKitLifecycleServiceRestart, StackKitLifecycleServiceLogs:
		return true
	default:
		return false
	}
}

func stackKitLifecycleCommandDataStatus(commandResult map[string]interface{}) string {
	data, ok := commandResult["data"].(map[string]interface{})
	if !ok {
		return ""
	}
	status, _ := data["status"].(string)
	return strings.ToLower(strings.TrimSpace(status))
}

// admitAdvancedTrustImport accepts a host's trust-import evidence only when it
// pins the exact bundle dispatched, then records the local Owner the host
// bound it to. Capabilities for the deployment are scoped to that Owner.
func admitAdvancedTrustImport(
	ctx context.Context,
	issuer AdvancedIssuer,
	req StackKitLifecycleRequest,
	stackKitStackID string,
	command *agentpb.StackKitCommand,
	result *agentpb.StackKitResult,
) (map[string]interface{}, error) {
	evidence, err := stackkitcommand.ParseAdvancedTrustImportEvidence(command, result)
	if err != nil {
		return nil, err
	}
	if issuer == nil {
		return nil, advancedissuer.ErrUnavailable
	}
	if err := issuer.RecordTrustBinding(ctx, advancedissuer.TrustBinding{
		TenantID: req.TenantID, DeploymentID: req.StackID, StackID: stackKitStackID,
		OwnerRef: evidence.OwnerRef, BundleSHA256: evidence.BundleSHA256,
	}); err != nil {
		return nil, fmt.Errorf("record advanced trust binding: %w", err)
	}
	return map[string]interface{}{
		"status": "imported", "owner_ref": evidence.OwnerRef,
		"bundle_sha256": evidence.BundleSHA256, "stackkit_stack_id": stackKitStackID,
	}, nil
}
