package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/logger"
)

type contextBoundStackKitCommandSender struct{}

func (contextBoundStackKitCommandSender) SendStackKitCommand(ctx context.Context, _ string, _ *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type recordingStackKitCommandSender struct {
	commands    []*agentpb.StackKitCommand
	applyStatus string
}

type failingStackKitCommandSender struct {
	commands []*agentpb.StackKitCommand
	failOn   agentpb.StackKitOperation
}

type agentTimeoutResultSender struct {
	commands []*agentpb.StackKitCommand
	failOn   agentpb.StackKitOperation
}

func (sender *agentTimeoutResultSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	if command.Operation == sender.failOn {
		return &agentpb.StackKitResult{
			Success: false, ExitCode: 1,
			Stderr:            "StackKits command timed out after 30s: context deadline exceeded",
			CommandResultJson: []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit generate","status":"failed","data":{"error":"StackKits command timed out after 30s: context deadline exceeded"}}`),
		}, nil
	}
	commandResult := typedStackKitFakeSuccessResult(command)
	return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
}

func (sender *failingStackKitCommandSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	if command.Operation == sender.failOn {
		return nil, context.DeadlineExceeded
	}
	commandResult := typedStackKitFakeSuccessResult(command)
	return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
}

type recordingManagedStackKitInventoryBuilder struct {
	called    bool
	err       error
	inventory []byte
	request   ManagedStackKitInventoryRequest
}

func (builder *recordingManagedStackKitInventoryBuilder) Build(_ context.Context, request ManagedStackKitInventoryRequest) ([]byte, error) {
	builder.called = true
	builder.request = request
	if builder.err != nil {
		return nil, builder.err
	}
	if len(builder.inventory) != 0 {
		return append([]byte(nil), builder.inventory...), nil
	}
	return []byte(`{"schemaVersion":"stackkit.inventory/v1"}`), nil
}

func (sender *recordingStackKitCommandSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	commandResult := typedStackKitFakeSuccessResult(command)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY && sender.applyStatus != "" {
		commandResult = typedApplyCommandResult(sender.applyStatus)
	}
	return &agentpb.StackKitResult{
		Success:           true,
		CommandResultJson: commandResult,
		Release:           command.Release,
	}, nil
}

func typedApplyCommandResult(status string) []byte {
	receipt, _ := json.Marshal(map[string]interface{}{
		"schemaVersion": "stackkit.command-result/v1", "command": "stackkit apply", "status": "success",
		"data": map[string]interface{}{"schemaVersion": "stackkit.apply-result/v2", "status": status},
	})
	return receipt
}

const testAdvancedTrustBundle = `{"keys":[{"issuerId":"techstack.test","keyId":"ed25519://sha256/` +
	`56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c","publicKey":"A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg"}],` +
	`"schemaVersion":"stackkit.advanced-trust-bundle/v1"}`

// staticAdvancedIssuer serves the test bundle and accepts every binding.
type staticAdvancedIssuer struct{}

func (staticAdvancedIssuer) TrustBundle() []byte { return []byte(testAdvancedTrustBundle) }
func (staticAdvancedIssuer) RecordTrustBinding(context.Context, advancedissuer.TrustBinding) error {
	return nil
}
func (staticAdvancedIssuer) TrustBindingFor(context.Context, string, string) (advancedissuer.TrustBinding, error) {
	return advancedissuer.TrustBinding{StackID: "cloud-stack", OwnerRef: "owner/local/00112233445566778899aabbccddeeff"}, nil
}
func (staticAdvancedIssuer) Issue(_ context.Context, request advancedissuer.Request) (advancedissuer.Capability, error) {
	return advancedissuer.Capability{ID: "capability-1", Operations: request.Operations, Raw: []byte(`{"allowedOperations":["` + strings.Join(request.Operations, `","`) + `"]}`)}, nil
}

// typedStackKitFakeSuccessResult answers like the pinned CLI for the
// operations whose evidence the rollout admits.
func typedStackKitFakeSuccessResult(command *agentpb.StackKitCommand) []byte {
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		return typedPlanCommandResult(testResolvedPlanHash)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT:
		digest := sha256.Sum256(command.AdvancedTrustBundle)
		receipt, _ := json.Marshal(map[string]interface{}{
			"schemaVersion": "stackkit.command-result/v1", "command": "stackkit advanced trust import", "status": "success",
			"data": map[string]interface{}{
				"schemaVersion": "stackkit.local-advanced-trust/v1", "bundleSHA256": "sha256:" + hex.EncodeToString(digest[:]),
				"ownerRef": "owner/local/00112233445566778899aabbccddeeff",
			},
		})
		return receipt
	default:
		return []byte(`{}`)
	}
}

func typedStackKitApplyRequest() StackKitLifecycleRequest {
	return StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
		CandidateSpecJSON:   []byte(`{"apiVersion":"stackkit/v2alpha2","metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"},"workloads":{"cloud-core":{"alternative":"standalone"}},"modules":{"stackkits-cloud-core-runtime":{"computeProfile":"standard"}}}`),
		AdvancedTrustBundle: []byte(testAdvancedTrustBundle),
	}
}

func TestTypedStackKitApplyPreservesCompletedDegradedStatus(t *testing.T) {
	sender := &recordingStackKitCommandSender{applyStatus: "completed_degraded"}
	request := typedStackKitApplyRequest()
	result, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t))
	if err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}
	if got := resultString(result, "status"); got != "completed_degraded" {
		t.Fatalf("status = %q, want completed_degraded", got)
	}
}

func TestTypedStackKitApplySequenceHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	request := typedStackKitApplyRequest()
	started := time.Now()
	_, err := runTypedStackKitApplySequence(ctx, contextBoundStackKitCommandSender{}, "job-1", request, releaseWithAdvancedCatalog(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runTypedStackKitApplySequence error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("sequence cancellation took %s, want below one second", elapsed)
	}
}

func TestTypedStackKitApplySequenceClassifiesOperationTimeouts(t *testing.T) {
	assertTimeout := func(sender StackKitCommandSender) {
		t.Helper()
		_, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", typedStackKitApplyRequest(), releaseWithAdvancedCatalog(t))
		var operationErr *typedStackKitOperationError
		if !errors.As(err, &operationErr) || !operationErr.TimedOut() || operationErr.Operation != StackKitLifecycleGenerate || operationErr.CommandID != "job-1-generate" {
			t.Fatalf("%T error = %v, want typed generate timeout for job-1-generate", sender, err)
		}
	}

	transportTimeout := &failingStackKitCommandSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE}
	assertTimeout(transportTimeout)
	if len(transportTimeout.commands) != 3 {
		t.Fatalf("commands = %d, want init, trust import and generate only", len(transportTimeout.commands))
	}
	assertTimeout(&agentTimeoutResultSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE})
}

// pacedStackKitCommandSender answers like failingStackKitCommandSender after
// spending real time on chosen operations, so a pre-apply phase can use up its
// shared deadline without a fake clock.
type pacedStackKitCommandSender struct {
	failingStackKitCommandSender
	pace map[agentpb.StackKitOperation]time.Duration
}

func (sender *pacedStackKitCommandSender) SendStackKitCommand(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	time.Sleep(sender.pace[command.Operation])
	return sender.failingStackKitCommandSender.SendStackKitCommand(ctx, agentID, command)
}

// A first apply on a cold node pulls images and waits for readiness for far
// longer than the pre-apply commands need, and the sequence killed it at seven
// minutes mid-change. Everything before apply shares ONE deadline, convergence
// waits included, and apply keeps its own write budget inside its command
// authority window whatever that phase used; only a closer job deadline still
// shrinks it.
func TestTypedStackKitApplyKeepsItsOwnBudgetWhateverThePreApplyPhaseUsed(t *testing.T) {
	// A shared deadline whose usable part, after the result grace every command
	// keeps back, is small enough to exhaust in real time.
	usable := 300 * time.Millisecond
	sequenceBudget := managedStackKitApplyAllowance + managedStackKitResultGrace + usable

	run := func(t *testing.T, ctx context.Context, budget time.Duration, pace map[agentpb.StackKitOperation]time.Duration) (*pacedStackKitCommandSender, error) {
		t.Helper()
		sender := &pacedStackKitCommandSender{
			failingStackKitCommandSender: failingStackKitCommandSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY},
			pace:                         pace,
		}
		_, err := runTypedStackKitApplySequenceWithBudget(ctx, sender, "job-1", typedStackKitApplyRequest(), releaseWithAdvancedCatalog(t), budget, nil)
		return sender, err
	}
	reachedApply := func(sender *pacedStackKitCommandSender) *agentpb.StackKitCommand {
		for _, command := range sender.commands {
			if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
				return command
			}
		}
		return nil
	}

	t.Run("apply keeps its whole write budget after pre-apply used its shared deadline", func(t *testing.T) {
		sender, _ := run(t, context.Background(), sequenceBudget, map[agentpb.StackKitOperation]time.Duration{
			agentpb.StackKitOperation_STACKKIT_OPERATION_INIT: usable / 2,
			agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN: usable / 3,
		})
		apply := reachedApply(sender)
		if apply == nil {
			t.Fatalf("the sequence never reached apply: %d commands", len(sender.commands))
		}
		got := time.Duration(apply.TimeoutSeconds) * time.Second
		if got != stackKitWriteCommandTimeout {
			t.Fatalf("apply agent timeout = %s, want the write budget %s", got, stackKitWriteCommandTimeout)
		}
		if got+managedStackKitResultGrace >= agentcontrol.CommandAuthorityLifetime {
			t.Fatalf("apply %s plus result grace outlives its command authority %s", got, agentcontrol.CommandAuthorityLifetime)
		}
	})

	t.Run("a pre-apply command cannot overrun the shared deadline into apply", func(t *testing.T) {
		sender, err := run(t, context.Background(), sequenceBudget, map[agentpb.StackKitOperation]time.Duration{
			agentpb.StackKitOperation_STACKKIT_OPERATION_INIT: 2 * usable,
		})
		var operationErr *typedStackKitOperationError
		if !errors.As(err, &operationErr) || !operationErr.TimedOut() || operationErr.Operation == StackKitLifecycleApply {
			t.Fatalf("error = %v, want a pre-apply timeout", err)
		}
		if reachedApply(sender) != nil {
			t.Fatalf("apply was dispatched after pre-apply ran out of its shared deadline")
		}
	})

	t.Run("a closer job deadline still shrinks apply", func(t *testing.T) {
		jobDeadline := 160 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), jobDeadline)
		defer cancel()
		sender, _ := run(t, ctx, 0, nil)
		apply := reachedApply(sender)
		if apply == nil {
			t.Fatalf("the sequence never reached apply: %d commands", len(sender.commands))
		}
		got := time.Duration(apply.TimeoutSeconds) * time.Second
		if got >= stackKitWriteCommandTimeout || got+managedStackKitResultGrace > jobDeadline {
			t.Fatalf("apply agent timeout = %s, want the remaining job deadline of %s", got, jobDeadline)
		}
	})
}

func TestTypedStackKitApplyWaitsWhenGuardRuntimeIsStillConverging(t *testing.T) {
	previousDelay := typedStackKitRuntimePendingRetryDelay
	typedStackKitRuntimePendingRetryDelay = 0
	t.Cleanup(func() { typedStackKitRuntimePendingRetryDelay = previousDelay })

	sender := &pendingThenReadyStackKitCommandSender{}
	request := typedStackKitApplyRequest()
	if _, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t)); err != nil {
		t.Fatalf("sequence should continue after Guard runtime readiness: %v", err)
	}
	if sender.initAttempts < 2 {
		t.Fatalf("init attempts = %d, want a retry after runtime convergence pending", sender.initAttempts)
	}
}

type pendingThenReadyStackKitCommandSender struct {
	initAttempts int
}

func (sender *pendingThenReadyStackKitCommandSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
		sender.initAttempts++
		if sender.initAttempts == 1 {
			return &agentpb.StackKitResult{
				Success: false, ExitCode: 1,
				Stderr: "runtime convergence is pending; retry after the Guard reports runtime readiness",
			}, nil
		}
	}
	commandResult := typedStackKitFakeSuccessResult(command)
	return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
}

func TestTypedStackKitApplyGeneratesAndPlansBeforeMutatingTheManagedNode(t *testing.T) {
	sender := &recordingStackKitCommandSender{}
	request := typedStackKitApplyRequest()
	request.Domain = "cloud.example"
	request.InventoryJSON = []byte(`{"schemaVersion":"stackkit.inventory/v1"}`)

	// A release that ships the Advanced catalog makes the first rollout
	// generate the Terramate target; change sets follow for later mutations.
	if _, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t)); err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}
	var initCandidate struct {
		Generation struct {
			Target string `json:"target"`
		} `json:"generation"`
	}
	if err := json.Unmarshal(sender.commands[0].CandidateSpecJson, &initCandidate); err != nil || initCandidate.Generation.Target != "terramate" {
		t.Fatalf("init candidate generation.target = %q (%v), want terramate", initCandidate.Generation.Target, err)
	}

	operations := make([]agentpb.StackKitOperation, 0, len(sender.commands))
	commandIDs := make([]string, 0, len(sender.commands))
	for _, command := range sender.commands {
		operations = append(operations, command.Operation)
		commandIDs = append(commandIDs, command.CommandId)
		if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT {
			if string(command.AdvancedTrustBundle) != testAdvancedTrustBundle || !command.OwnerApproved {
				t.Fatalf("trust import = %+v, want the Owner-approved installation bundle", command)
			}
			continue
		}
		if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_INIT && string(command.InventoryJson) != string(request.InventoryJSON) {
			t.Fatalf("%s Inventory = %s, want %s", command.CommandId, command.InventoryJson, request.InventoryJSON)
		}
	}
	wantOperations := []agentpb.StackKitOperation{
		agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", operations, wantOperations)
	}
	if got := sender.commands[len(sender.commands)-1].ExpectedPlanHash; got != testResolvedPlanHash {
		t.Fatalf("apply expected_plan_hash = %q, want %q", got, testResolvedPlanHash)
	}
	if want := []string{"job-1-init", "job-1-advanced_trust_import", "job-1-generate", "job-1-plan", "job-1-apply"}; !reflect.DeepEqual(commandIDs, want) {
		t.Fatalf("command IDs = %v, want %v", commandIDs, want)
	}
}

func TestTypedStackKitApplyBindsManagedAddressBeforeNodeGeneration(t *testing.T) {
	sender := &recordingStackKitCommandSender{}
	request := typedStackKitApplyRequest()
	request.AddressPrefix = "sh-demo-ab12"
	request.BoundSpecPath = "stack-spec.address-bound.v2.json"
	if _, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t)); err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}
	want := []agentpb.StackKitOperation{
		agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_ADDRESS_BIND,
		agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
	}
	got := make([]agentpb.StackKitOperation, 0, len(sender.commands))
	for _, command := range sender.commands {
		got = append(got, command.Operation)
	}
	if !reflect.DeepEqual(got, want) || sender.commands[2].SpecPath != "stack-spec.yaml" || sender.commands[3].SpecPath != request.BoundSpecPath || sender.commands[5].SpecPath != request.BoundSpecPath {
		t.Fatalf("bound lifecycle commands = %+v, want bind before generated %q", sender.commands, request.BoundSpecPath)
	}
}

func TestTypedManagedRolloutCompletesOwnerHandoffFromTechstackFacts(t *testing.T) {
	// Regression (kombify-Techstack-3pa8): a verified typed rollout for the
	// Wizard's automatic cloud Owner failed because the v2 apply result
	// carries no identity outputs.
	sender := &recordingStackKitCommandSender{applyStatus: "applied"}
	applyResult, err := runTypedStackKitApplySequence(context.Background(), sender, "job-owner-handoff", typedStackKitApplyRequest(), releaseWithAdvancedCatalog(t))
	if err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}
	queue := NewQueue(1, logger.New("error", ""))
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-owner-handoff", TargetID: "stack-1", Result: map[string]interface{}{}, Payload: map[string]interface{}{
		"owner_spec_bootstrap":    map[string]interface{}{"endpoint": "https://techstack.example/api/v1/stacks/stack-1/owner-spec", "token": "bootstrap-token", "expires_at": "2026-09-25T00:00:00Z"},
		StackOwnerEmailPayloadKey: "owner@example.com",
	}}
	queue.jobs[job.ID] = job
	rollout := &deployRollout{
		cfg: &ProvisionConfig{StackKitCommander: sender}, job: job, q: queue, managedRuntime: true,
		actionReq: RuntimeActionRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1"},
		e2eProof:  map[string]any{}, runtimeProof: map[string]interface{}{},
		stackKitOutputs: map[string]interface{}{}, runtimeMetrics: map[string]string{},
		finalRuntimePhase: RuntimePhaseVerified,
	}
	mergeStackKitOutputs(rollout.stackKitOutputs, applyResult)

	err = rollout.finalize(context.Background(), &deployPreparation{managedRuntime: true}, &deployArtifacts{
		unifiedSpec: &core.UnifiedSpec{StackKit: "cloud-kit"},
		metadata:    map[string]string{metadataKeyKombifyMeLoginHost: "auth.sh-demo-ab12.kombify.me"},
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	outputs := mapFromInterface(job.Snapshot().Result[metadataKeyStackKitOutputs])
	owner := mapFromInterface(mapFromInterface(outputs["identity"])["owner"])
	gateway := mapFromInterface(outputs["login_gateway"])
	if owner["username"] != "owner@example.com" || gateway["url"] != "https://auth.sh-demo-ab12.kombify.me" {
		t.Fatalf("stackkit_outputs = %#v, want the Owner login and login gateway", outputs)
	}
}

func TestManagedStackKitInventorySkipsUnselectedOptionalBackupAndContinuesCoreApply(t *testing.T) {
	builder := &recordingManagedStackKitInventoryBuilder{
		inventory: []byte(`{"schemaVersion":"stackkit.inventory/v1","executionChannels":{"host-channel-cloud-main":{"apiVersion":"stackkit.standard-execution-channel/v1","kind":"StandardExecutionChannel","channelRef":"host-channel-cloud-main","siteRef":"cloud","nodeRef":"cloud-main","operationClass":"standard","operationsProcess":{"executable":"/usr/local/libexec/techstack-stackkit-operations","executableSha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}`),
	}
	job := &Job{ID: "job-optional-backup", Result: map[string]interface{}{}}
	rollout := &deployRollout{
		cfg:       &ProvisionConfig{ManagedStackKitInventory: builder},
		job:       job,
		actionReq: RuntimeActionRequest{TenantID: "tenant-1"},
	}

	inventory, delivery, err := rollout.buildManagedStackKitInventory(
		context.Background(),
		[]byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{}}`),
	)
	if err != nil {
		t.Fatalf("an unselected optional backup must not block core rollout: %v", err)
	}
	if delivery != nil {
		t.Fatal("an unselected optional backup scheduled a backup delivery")
	}
	if !builder.called {
		t.Fatal("unselected optional backup did not reach the channel-only Inventory builder")
	}
	if !strings.Contains(string(inventory), "executionChannels") {
		t.Fatalf("Inventory = %s, want the managed Operations channel", inventory)
	}
	receipt, ok := job.Result["managed_backup_target_inventory"].(map[string]interface{})
	if !ok || receipt["status"] != "not_applicable" || receipt["reason_code"] != "optional_backup_not_selected" || receipt["retryable"] != false {
		t.Fatalf("backup receipt = %#v, want observable not_applicable optional-backup receipt", job.Result["managed_backup_target_inventory"])
	}

	sender := &recordingStackKitCommandSender{}
	request := typedStackKitApplyRequest()
	request.InventoryJSON = inventory
	if _, err := runTypedStackKitApplySequence(context.Background(), sender, job.ID, request, releaseWithAdvancedCatalog(t)); err != nil {
		t.Fatalf("core apply did not continue without an optional backup Inventory: %v", err)
	}
	if len(sender.commands) == 0 || sender.commands[len(sender.commands)-1].Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		t.Fatalf("core rollout did not reach Apply: %#v", sender.commands)
	}
	if command := sender.commands[len(sender.commands)-1]; string(command.InventoryJson) != string(inventory) {
		t.Fatalf("Apply did not retain the managed Operations channel Inventory: %s", command.InventoryJson)
	}
}

func TestManagedStackKitInventoryFailsClosedWithObservableReceipt(t *testing.T) {
	tests := []struct {
		name, resolvedPlan, wantReason string
		builder                        ManagedStackKitInventoryBuilder
	}{
		{
			name:         "selected backup without a delivery authority",
			resolvedPlan: `{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{"cloud":{"offsite-object-backup":{"requirementsHash":"sha256:required"}}}}`,
			builder:      &recordingManagedStackKitInventoryBuilder{},
			wantReason:   "managed_backup_delivery_unavailable",
		},
		{
			name:         "malformed backup requirements",
			resolvedPlan: `{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":["cloud"]}`,
			builder:      &recordingManagedStackKitInventoryBuilder{},
			wantReason:   "backup_target_requirements_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := &Job{ID: "job-backup-failure", Result: map[string]interface{}{}}
			rollout := &deployRollout{
				cfg:       &ProvisionConfig{ManagedStackKitInventory: test.builder},
				job:       job,
				actionReq: RuntimeActionRequest{TenantID: "tenant-1"},
			}

			if _, _, err := rollout.buildManagedStackKitInventory(context.Background(), []byte(test.resolvedPlan)); err == nil {
				t.Fatal("expected fail-closed backup inventory error")
			}
			receipt, ok := job.Result["managed_backup_target_inventory"].(map[string]interface{})
			if !ok || receipt["status"] != "failed" || receipt["reason_code"] != test.wantReason || receipt["retryable"] != false {
				t.Fatalf("backup receipt = %#v, want failed reason %q", job.Result["managed_backup_target_inventory"], test.wantReason)
			}
		})
	}
}

func typedPlanCommandResult(planHash string) []byte {
	inspection, _ := json.Marshal(map[string]interface{}{
		"apiVersion": "stackkit.plan-inspection/v1", "kind": "PlanInspection", "verifiedPhase": "generation",
		"binding":         map[string]interface{}{"planHash": planHash},
		"readiness":       map[string]interface{}{"generation": map[string]interface{}{"status": "ready", "blockers": []string{}}},
		"executorInvoked": false,
	})
	receipt, _ := json.Marshal(map[string]interface{}{
		"schemaVersion": "stackkit.command-result/v1", "command": "stackkit plan", "status": "success",
		"data": map[string]interface{}{"output": string(inspection)},
	})
	return receipt
}

func TestModernRolloutInventoryNamesAChannelForEveryNode(t *testing.T) {
	builder := &recordingManagedStackKitInventoryBuilder{}
	rollout := &deployRollout{
		cfg:         &ProvisionConfig{ManagedStackKitInventory: builder},
		job:         &Job{ID: "job-modern", Result: map[string]interface{}{}},
		unifiedSpec: &core.UnifiedSpec{StackKit: "modern-homelab"},
		actionReq:   RuntimeActionRequest{TenantID: "tenant-1"},
	}
	if _, _, err := rollout.buildManagedStackKitInventory(context.Background(),
		[]byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{}}`)); err != nil {
		t.Fatal(err)
	}
	channels := map[string]string{}
	for _, node := range builder.request.Nodes {
		channels[node.SiteRef+"/"+node.NodeRef] = node.ChannelRef
	}
	if channels["home/home-main"] != "local-home-main" || channels["cloud/cloud-edge"] != "local-cloud-edge" || len(channels) != 2 {
		t.Fatalf("Home's Apply needs a channel for the Cloud node too, got %v", channels)
	}
}
