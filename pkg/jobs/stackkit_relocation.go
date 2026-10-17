package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// JobTypeWorkloadRelocation moves PaaS workloads (Coolify projects) from one
// managed server to another. Each step runs the pinned StackKits CLI on the
// node it concerns; Techstack orders them, carries the secret-free plan
// between the nodes, and has the kombify Wallet deliver the move's transfer
// secret to both.
const JobTypeWorkloadRelocation JobType = "workload_relocation"

// WorkloadRelocationResultField holds the secret-free outcome of a move.
const WorkloadRelocationResultField = "workload_relocation"

const (
	workloadRelocationPayloadField = "workload_relocation"
	// The drain waits up to two minutes for containers to stop and the
	// undrain until the restarted resources answer.
	stackKitRelocationDrainTimeout = 10 * time.Minute
	// Send backs up and shares; receive waits for the share, downloads and
	// restores. Both must return inside the command authority window, so a
	// move larger than this budget needs a detached transfer (not yet built).
	stackKitRelocationTransferTimeout = 12 * time.Minute
)

// A transfer step and its result grace end inside its command authority
// window with the managed margin to spare.
const _ = uint64(15*time.Minute - stackKitRelocationTransferTimeout - managedStackKitResultGrace - managedStackKitAuthorityMargin)

// defaultRelocationTestDomainSuffixes are the only domains a move may switch
// restored resources to until real-host acceptance admits production domains:
// reserved test names and the wildcard-IP lab services. A switch changes the
// destination's Coolify configuration only; no DNS record is touched.
var defaultRelocationTestDomainSuffixes = []string{".test", ".example", ".invalid", ".localhost", ".sslip.io", ".nip.io"}

// WorkloadRelocationNode is one side of a move, bound at enqueue from the
// stack's managed rollout and its admitted agent.
type WorkloadRelocationNode struct {
	StackID            string `json:"stack_id"`
	StackKitInstanceID string `json:"stackkit_instance_id"`
	AgentID            string `json:"agent_id"`
	NodeID             string `json:"node_id"`
	NodeRef            string `json:"node_ref"`
	StackKit           string `json:"stackkit"`
	StackName          string `json:"stack_name"`
	WorkingDirectory   string `json:"working_directory"`
	SpecPath           string `json:"spec_path"`
}

// WorkloadRelocationRequest is the closed input of a move. It names the
// Wallet item holding the move's transfer secret, never a secret value. The
// owner lists both nodes as host targets on that item.
type WorkloadRelocationRequest struct {
	TenantID         string                 `json:"tenant_id"`
	OwnerID          string                 `json:"owner_id"`
	Source           WorkloadRelocationNode `json:"source"`
	Destination      WorkloadRelocationNode `json:"destination"`
	Projects         []string               `json:"projects"`
	SourceAddress    string                 `json:"source_address"`
	DomainSwitches   []string               `json:"domain_switches,omitempty"`
	WalletItemID     string                 `json:"wallet_item_id"`
	WalletItemRotate bool                   `json:"wallet_item_rotate,omitempty"`
	OwnerApproved    bool                   `json:"owner_approved"`
}

// NormalizeWorkloadRelocationRequest admits a move: two different agents of
// one owner, the projects, the source address, the Wallet item and only
// domain switches to test domains.
func NormalizeWorkloadRelocationRequest(req WorkloadRelocationRequest, testDomainSuffixes []string) (WorkloadRelocationRequest, error) {
	req.TenantID, req.OwnerID = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.OwnerID)
	req.WalletItemID = strings.TrimSpace(req.WalletItemID)
	if !req.OwnerApproved {
		return req, fmt.Errorf("moving workloads stops them on the source and requires explicit Owner approval")
	}
	if req.TenantID == "" || req.OwnerID == "" {
		return req, fmt.Errorf("tenant and Owner are required")
	}
	for _, node := range []*WorkloadRelocationNode{&req.Source, &req.Destination} {
		node.StackID, node.AgentID = strings.TrimSpace(node.StackID), strings.TrimSpace(node.AgentID)
		if node.StackID == "" || node.AgentID == "" {
			return req, fmt.Errorf("source and destination each need their stack and agent")
		}
		if node.WorkingDirectory == "" {
			node.WorkingDirectory = defaultStackKitNodeWorkspace
		}
	}
	if req.Source.AgentID == req.Destination.AgentID {
		return req, fmt.Errorf("source and destination must be different servers")
	}
	projects := make([]string, 0, len(req.Projects))
	for _, project := range req.Projects {
		projects = append(projects, strings.TrimSpace(project))
	}
	req.Projects = projects
	address, err := stackkitcommand.NormalizeRelocationHost(req.SourceAddress)
	if err != nil {
		return req, err
	}
	req.SourceAddress = address
	if !stackkitcommand.ValidWalletItemRef(req.WalletItemID) {
		return req, fmt.Errorf("a move requires the Wallet item id (itm_...) of its transfer secret")
	}
	if len(testDomainSuffixes) == 0 {
		testDomainSuffixes = defaultRelocationTestDomainSuffixes
	}
	switches := make([]string, 0, len(req.DomainSwitches))
	for _, value := range req.DomainSwitches {
		oldHost, newHost, err := stackkitcommand.ParseRelocationDomainSwitch(value)
		if err != nil {
			return req, err
		}
		if !hasRelocationTestDomainSuffix(newHost, testDomainSuffixes) {
			return req, fmt.Errorf("domain switch to %s refused: a move switches only to test domains (%s) until real-host acceptance", newHost, strings.Join(testDomainSuffixes, ", "))
		}
		switches = append(switches, oldHost+"="+newHost)
	}
	req.DomainSwitches = switches
	// The step requests carry the rest of the validation: projects for plan.
	if _, err := NormalizeStackKitLifecycleRequest(req.step(req.Source, stackKitStepRelocatePlan)); err != nil {
		return req, err
	}
	return req, nil
}

func hasRelocationTestDomainSuffix(host string, suffixes []string) bool {
	for _, suffix := range suffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix != "" && strings.HasSuffix(host, "."+strings.TrimPrefix(suffix, ".")) {
			return true
		}
	}
	return false
}

func isRelocationStep(operation string) bool {
	switch operation {
	case stackKitStepRelocatePlan, stackKitStepRelocateCheck, stackKitStepRelocateDrain,
		stackKitStepRelocateUndrain, stackKitStepRelocateSend, stackKitStepRelocateReceive:
		return true
	default:
		return false
	}
}

func isRelocationTransferStep(operation string) bool {
	return operation == stackKitStepRelocateSend || operation == stackKitStepRelocateReceive
}

// validateRelocationStepRequest checks the inputs of one step; the
// stackkitcommand validation repeats it on the wire command.
func validateRelocationStepRequest(req *StackKitLifecycleRequest) error {
	if req.DryRun || req.Offline || len(req.CandidateSpecJSON) != 0 || len(req.InventoryJSON) != 0 {
		return fmt.Errorf("%s takes no dry run, offline mode, candidate or Inventory", req.Operation)
	}
	readOnly := req.Operation == stackKitStepRelocatePlan || req.Operation == stackKitStepRelocateCheck
	if !readOnly && !req.OwnerApproved {
		return fmt.Errorf("%s requires explicit Owner approval", req.Operation)
	}
	if readOnly {
		req.OwnerApproved = false
	}
	switch req.Operation {
	case stackKitStepRelocatePlan, stackKitStepRelocateSend:
		if len(req.RelocationProjects) == 0 {
			return fmt.Errorf("%s requires the Coolify projects to move", req.Operation)
		}
	case stackKitStepRelocateCheck, stackKitStepRelocateDrain:
		if err := stackkitcommand.ValidateRelocationPlan(req.RelocationPlanJSON); err != nil {
			return err
		}
	}
	if isRelocationTransferStep(req.Operation) && (req.RelocationSourceAddress == "" || !stackkitcommand.ValidWalletItemRef(req.WalletItemID)) {
		return fmt.Errorf("%s requires the source address and the move's Wallet item", req.Operation)
	}
	return nil
}

// step is the lifecycle request of one step on node: only that step's inputs.
func (req WorkloadRelocationRequest) step(node WorkloadRelocationNode, operation string) StackKitLifecycleRequest {
	step := StackKitLifecycleRequest{
		StackID: node.StackID, StackKitInstanceID: node.StackKitInstanceID, TenantID: req.TenantID, OwnerID: req.OwnerID,
		AgentID: node.AgentID, NodeID: node.NodeID, NodeRef: node.NodeRef, StackKit: node.StackKit, StackName: node.StackName,
		WorkingDirectory: node.WorkingDirectory, SpecPath: node.SpecPath, Operation: operation, OwnerApproved: true,
	}
	switch operation {
	case stackKitStepRelocatePlan:
		step.RelocationProjects, step.OwnerApproved = append([]string(nil), req.Projects...), false
	case stackKitStepRelocateCheck:
		step.OwnerApproved = false
	case stackKitStepRelocateSend:
		step.RelocationProjects = append([]string(nil), req.Projects...)
		step.RelocationSourceAddress, step.WalletItemID = req.SourceAddress, req.WalletItemID
	case stackKitStepRelocateReceive:
		step.RelocationSourceAddress, step.WalletItemID = req.SourceAddress, req.WalletItemID
		step.RelocationSetDomains = append([]string(nil), req.DomainSwitches...)
	}
	return step
}

// WorkloadRelocationPayload is the persisted job payload of a move.
func WorkloadRelocationPayload(req WorkloadRelocationRequest) (map[string]interface{}, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	return map[string]interface{}{workloadRelocationPayloadField: payload}, nil
}

func workloadRelocationRequestFromJob(job *Job) (WorkloadRelocationRequest, error) {
	var req WorkloadRelocationRequest
	encoded, err := json.Marshal(job.Snapshot().Payload[workloadRelocationPayloadField])
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(encoded, &req); err != nil {
		return req, fmt.Errorf("decode workload relocation payload: %w", err)
	}
	return req, nil
}

// RegisterWorkloadRelocationHandler registers the move with the lifecycle
// configuration: its sender, release and Wallet host delivery.
func RegisterWorkloadRelocationHandler(q *Queue, cfg StackKitLifecycleConfig) {
	q.RegisterHandler(JobTypeWorkloadRelocation, WorkloadRelocationHandler(cfg))
}

// WorkloadRelocationHandler runs a move: Wallet item to both nodes, then
// plan (source), check (destination), drain (source), send and receive
// together, with the destination's domain switches applied by receive. A
// failure after the drain starts the source's workloads again (undrain).
func WorkloadRelocationHandler(cfg StackKitLifecycleConfig) JobHandler {
	return func(ctx context.Context, job *Job, q *Queue) error {
		if cfg.Sender == nil {
			return NewConfigError(fmt.Errorf("typed StackKits dispatcher is not configured"))
		}
		req, err := workloadRelocationRequestFromJob(job)
		if err == nil {
			req, err = NormalizeWorkloadRelocationRequest(req, cfg.RelocationTestDomainSuffixes)
		}
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
		return runWorkloadRelocation(ctx, cfg, job, q, req, *release)
	}
}

func runWorkloadRelocation(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req WorkloadRelocationRequest, release stackkitrelease.Release) error {
	outcome := map[string]interface{}{
		"projects": req.Projects, "source_stack_id": req.Source.StackID, "destination_stack_id": req.Destination.StackID,
		"source_agent_id": req.Source.AgentID, "destination_agent_id": req.Destination.AgentID,
		"domain_switches": req.DomainSwitches, "wallet_item_id": req.WalletItemID,
	}
	summary := map[string]interface{}{
		"schema_version": "techstack.workload-relocation-result/v1", WorkloadRelocationResultField: outcome,
	}
	finish := func(err error) error {
		summary["success"] = err == nil
		job.replaceResult(summary)
		if err == nil {
			return nil
		}
		var config *aiProviderConfigError
		if errors.As(err, &config) {
			return NewConfigError(config.err)
		}
		return NewPermanentError(err)
	}
	if version := release.Receipt().Version; !stackkitcommand.ReleaseAtLeast(version, stackkitcommand.MinWorkloadRelocationRelease) {
		outcome["reason_code"] = "stackkits_release_too_old"
		return finish(&aiProviderConfigError{fmt.Errorf("moving workloads needs StackKits %s or newer; this installation pins %s", stackkitcommand.MinWorkloadRelocationRelease, version)})
	}

	// 1. The move's transfer secret reaches both nodes through the Wallet.
	for _, side := range []struct {
		name string
		node WorkloadRelocationNode
	}{{"source", req.Source}, {"destination", req.Destination}} {
		base := req.step(side.node, stackKitStepRelocateUndrain)
		base.WalletItemID, base.WalletItemRotate = req.WalletItemID, req.WalletItemRotate
		delivered := map[string]interface{}{}
		target, version, err := deliverWalletItem(ctx, cfg, job, q, base, release, delivered)
		outcome[side.name+"_wallet_delivery"] = delivered
		if err != nil {
			return finish(fmt.Errorf("the move's Wallet item did not reach the %s server: %w", side.name, err))
		}
		delivered["host_target"], delivered["item_version"] = hostTargetMap(target), version
	}

	// 2. Plan on the source, check on the destination: nothing changes yet.
	job.setStep("stackkit_" + stackKitStepRelocatePlan)
	q.UpdateProgress(job.ID, 50, "Planning the move on the source server")
	plan, err := relocationPlan(ctx, cfg, job, req, release)
	if err != nil {
		return finish(err)
	}
	job.setStep("stackkit_" + stackKitStepRelocateCheck)
	q.UpdateProgress(job.ID, 55, "Checking that the move can land on the destination server")
	check, err := relocationCheck(ctx, cfg, job, req, release, plan)
	if check != nil {
		outcome["check"] = check
	}
	if err != nil {
		return finish(err)
	}

	// 3. Drain the source: from here on a failure starts it again.
	job.setStep("stackkit_" + stackKitStepRelocateDrain)
	q.UpdateProgress(job.ID, 60, "Stopping the moving applications on the source server")
	drain := req.step(req.Source, stackKitStepRelocateDrain)
	drain.RelocationPlanJSON = plan
	var drainCommand *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, drain, release, &drainCommand)
	if err == nil {
		var record stackkitcommand.RelocationDrain
		record, err = stackkitcommand.ParseRelocationDrain(drainCommand, result)
		if err == nil {
			outcome["drained"] = record.Resources
			if !record.Complete {
				err = fmt.Errorf("the source could not stop every moving resource")
			}
		}
		if err != nil {
			err = &typedStackKitOperationError{Operation: stackKitStepRelocateDrain, CommandID: commandID, Err: err}
		}
	}
	if err != nil {
		return finish(relocationRollback(ctx, cfg, job, q, req, release, outcome, err))
	}

	// 4. Send and receive together: receive waits for the source's share.
	job.setStep("stackkit_relocate_transfer")
	q.UpdateProgress(job.ID, 70, "Moving the data from the source to the destination server")
	sendErr, receiveErr := relocationTransfer(ctx, cfg, job, req, release)
	if sendErr != nil {
		outcome["send_error"] = sendErr.Error()
	}
	if receiveErr != nil {
		return finish(relocationRollback(ctx, cfg, job, q, req, release, outcome, receiveErr))
	}
	// The destination restored and started the move; the source stays
	// stopped with nothing deleted until the owner removes it there.
	outcome["state"] = "moved"
	q.UpdateProgress(job.ID, 100, "The workloads run on the destination server")
	return finish(nil)
}

func relocationPlan(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, req WorkloadRelocationRequest, release stackkitrelease.Release) ([]byte, error) {
	var command *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, req.step(req.Source, stackKitStepRelocatePlan), release, &command)
	if err != nil {
		return nil, err
	}
	plan, err := stackkitcommand.ParseRelocationPlan(command, result)
	if err != nil {
		return nil, &typedStackKitOperationError{Operation: stackKitStepRelocatePlan, CommandID: commandID, Err: err}
	}
	return plan, nil
}

// relocationCheck returns the destination's verdict; a blocked check is an
// error that still carries its blockers.
func relocationCheck(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, req WorkloadRelocationRequest, release stackkitrelease.Release, plan []byte) (*stackkitcommand.RelocationCheck, error) {
	step := req.step(req.Destination, stackKitStepRelocateCheck)
	step.RelocationPlanJSON = plan
	var command *agentpb.StackKitCommand
	result, commandID, dispatchErr := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, step, release, &command)
	if result == nil {
		return nil, dispatchErr
	}
	check, err := stackkitcommand.ParseRelocationCheck(command, result)
	if err != nil {
		if dispatchErr != nil {
			return nil, dispatchErr
		}
		return nil, &typedStackKitOperationError{Operation: stackKitStepRelocateCheck, CommandID: commandID, Err: err}
	}
	if !check.Ready {
		return &check, fmt.Errorf("the destination server cannot take the move yet: %s", strings.Join(check.Blockers, "; "))
	}
	return &check, nil
}

// relocationTransfer runs send on the source and receive on the destination
// at the same time and returns each side's error.
func relocationTransfer(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, req WorkloadRelocationRequest, release stackkitrelease.Release) (sendErr, receiveErr error) {
	run := func(node WorkloadRelocationNode, operation string) error {
		var command *agentpb.StackKitCommand
		result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, req.step(node, operation), release, &command)
		if err != nil {
			return err
		}
		if err := stackkitcommand.ValidateRelocationStep(command, result); err != nil {
			return &typedStackKitOperationError{Operation: operation, CommandID: commandID, Err: err}
		}
		return nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sendErr = run(req.Source, stackKitStepRelocateSend) }()
	go func() { defer wg.Done(); receiveErr = run(req.Destination, stackKitStepRelocateReceive) }()
	wg.Wait()
	return sendErr, receiveErr
}

// relocationRollback starts the drained workloads on the source again and
// reports the failure that caused it, and a failed rollback with it.
func relocationRollback(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req WorkloadRelocationRequest, release stackkitrelease.Release, outcome map[string]interface{}, cause error) error {
	job.setStep("stackkit_" + stackKitStepRelocateUndrain)
	q.UpdateProgress(job.ID, 90, "Starting the applications on the source server again")
	var command *agentpb.StackKitCommand
	result, commandID, err := dispatchManagedDelivery(ctx, cfg.Sender, job.ID, req.step(req.Source, stackKitStepRelocateUndrain), release, &command)
	if err == nil {
		err = stackkitcommand.ValidateRelocationStep(command, result)
		if err != nil {
			err = &typedStackKitOperationError{Operation: stackKitStepRelocateUndrain, CommandID: commandID, Err: err}
		}
	}
	if err != nil {
		outcome["state"] = "rollback_failed"
		return fmt.Errorf("%w; starting the source again failed too, run `stackkit relocate drain --undo --owner-approve` there: %v", cause, err)
	}
	outcome["state"] = "rolled_back"
	return fmt.Errorf("%w; the source runs again", cause)
}
