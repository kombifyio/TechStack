package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
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
	commandResult := []byte(`{}`)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		commandResult = typedPlanCommandResult(testResolvedPlanHash)
	}
	return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
}

func (sender *failingStackKitCommandSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	sender.commands = append(sender.commands, command)
	if command.Operation == sender.failOn {
		return nil, context.DeadlineExceeded
	}
	commandResult := []byte(`{}`)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		commandResult = typedPlanCommandResult(testResolvedPlanHash)
	}
	return &agentpb.StackKitResult{Success: true, CommandResultJson: commandResult, Release: command.Release}, nil
}

type recordingManagedStackKitInventoryBuilder struct {
	called    bool
	err       error
	inventory []byte
}

func (builder *recordingManagedStackKitInventoryBuilder) Build(_ context.Context, _ ManagedStackKitInventoryRequest) ([]byte, error) {
	builder.called = true
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
	commandResult := []byte(`{}`)
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		commandResult = typedPlanCommandResult(testResolvedPlanHash)
	} else if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY && sender.applyStatus != "" {
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

func TestTypedStackKitApplyPreservesCompletedDegradedStatus(t *testing.T) {
	sender := &recordingStackKitCommandSender{applyStatus: "completed_degraded"}
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
	}
	result, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, stackkitrelease.Release{})
	if err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}
	if got := resultString(result, "status"); got != "completed_degraded" {
		t.Fatalf("status = %q, want completed_degraded", got)
	}
}

func TestTypedStackKitDispatchHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := sendStackKitCommandBounded(ctx, contextBoundStackKitCommandSender{}, "agent-1", &agentpb.StackKitCommand{
		CommandId:      "command-1",
		TimeoutSeconds: int32(stackKitWriteCommandTimeout / time.Second),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sendStackKitCommandBounded error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("parent cancellation took %s, want below one second", elapsed)
	}
}

func TestTypedStackKitApplySequenceHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
	}
	started := time.Now()
	_, err := runTypedStackKitApplySequence(ctx, contextBoundStackKitCommandSender{}, "job-1", request, stackkitrelease.Release{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runTypedStackKitApplySequence error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("sequence cancellation took %s, want below one second", elapsed)
	}
}

func TestTypedStackKitApplySequenceStopsAfterOperationDeadline(t *testing.T) {
	sender := &failingStackKitCommandSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE}
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
	}
	_, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, stackkitrelease.Release{})
	var operationErr *typedStackKitOperationError
	if !errors.As(err, &operationErr) || !operationErr.TimedOut() {
		t.Fatalf("error = %v, want typed operation timeout", err)
	}
	if operationErr.Operation != StackKitLifecycleGenerate || operationErr.CommandID != "job-1-generate" {
		t.Fatalf("operation error = %+v", operationErr)
	}
	if len(sender.commands) != 2 {
		t.Fatalf("commands = %d, want init and generate only", len(sender.commands))
	}
}

func TestTypedStackKitApplySequenceShrinksAgentBudgetToJobDeadline(t *testing.T) {
	sender := &failingStackKitCommandSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY}
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
	}
	_, _ = runTypedStackKitApplySequenceWithBudget(context.Background(), sender, "job-1", request, stackkitrelease.Release{}, 160*time.Second)
	if len(sender.commands) != 4 {
		t.Fatalf("commands = %d, want four", len(sender.commands))
	}
	if got := time.Duration(sender.commands[3].TimeoutSeconds) * time.Second; got >= managedStackKitApplyTimeout || got > 150*time.Second {
		t.Fatalf("apply agent timeout = %s, want remaining job budget", got)
	}
}

func TestAgentTimeoutResultIsTypedForDiagnostics(t *testing.T) {
	sender := &agentTimeoutResultSender{failOn: agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE}
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
	}
	_, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, stackkitrelease.Release{})
	var operationErr *typedStackKitOperationError
	if !errors.As(err, &operationErr) || !operationErr.TimedOut() {
		t.Fatalf("error = %v, want classified agent timeout", err)
	}
	if operationErr.Operation != StackKitLifecycleGenerate || operationErr.CommandID != "job-1-generate" {
		t.Fatalf("operation error = %+v", operationErr)
	}
}

func TestTypedStackKitApplyGeneratesAndPlansBeforeMutatingTheManagedNode(t *testing.T) {
	sender := &recordingStackKitCommandSender{}
	request := StackKitLifecycleRequest{
		StackID:          "stack-1",
		TenantID:         "tenant-1",
		OwnerID:          "owner-1",
		AgentID:          "agent-1",
		OwnerApproved:    true,
		WorkingDirectory: "/opt/stackkit",
		SpecPath:         "stack-spec.yaml",
		StackKit:         "cloud-kit",
		StackName:        "cloud-stack",
		Domain:           "cloud.example",
		InventoryJSON:    []byte(`{"schemaVersion":"stackkit.inventory/v1"}`),
	}

	if _, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, stackkitrelease.Release{}); err != nil {
		t.Fatalf("runTypedStackKitApplySequence: %v", err)
	}

	operations := make([]agentpb.StackKitOperation, 0, len(sender.commands))
	commandIDs := make([]string, 0, len(sender.commands))
	for _, command := range sender.commands {
		operations = append(operations, command.Operation)
		commandIDs = append(commandIDs, command.CommandId)
		if string(command.InventoryJson) != string(request.InventoryJSON) {
			t.Fatalf("%s Inventory = %s, want %s", command.CommandId, command.InventoryJson, request.InventoryJSON)
		}
	}
	wantOperations := []agentpb.StackKitOperation{
		agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
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
	if want := []string{"job-1-init", "job-1-generate", "job-1-plan", "job-1-apply"}; !reflect.DeepEqual(commandIDs, want) {
		t.Fatalf("command IDs = %v, want %v", commandIDs, want)
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

	inventory, err := rollout.buildManagedStackKitInventory(
		context.Background(),
		[]byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{}}`),
		"v0.16.1", "sha256:"+strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatalf("an unselected optional backup must not block core rollout: %v", err)
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
	request := StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true,
		WorkingDirectory: "/opt/stackkit", SpecPath: "stack-spec.yaml", StackKit: "cloud-kit", StackName: "cloud-stack",
		InventoryJSON: inventory,
	}
	if _, err := runTypedStackKitApplySequence(context.Background(), sender, job.ID, request, stackkitrelease.Release{}); err != nil {
		t.Fatalf("core apply did not continue without an optional backup Inventory: %v", err)
	}
	if len(sender.commands) != 4 || sender.commands[3].Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		t.Fatalf("commands = %#v, want init/generate/plan/apply", sender.commands)
	}
	for _, command := range sender.commands {
		if !strings.Contains(string(command.InventoryJson), "executionChannels") {
			t.Fatalf("%s omitted the managed Operations channel Inventory: %s", command.CommandId, command.InventoryJson)
		}
	}
}

func TestManagedStackKitInventoryFailsClosedForExplicitBackupAttestationFailure(t *testing.T) {
	builder := &recordingManagedStackKitInventoryBuilder{
		err: errors.New("write managed backup target sentinel: S3 PutObject returned 401 Unauthorized"),
	}
	job := &Job{ID: "job-explicit-backup", Result: map[string]interface{}{}}
	rollout := &deployRollout{
		cfg:       &ProvisionConfig{ManagedStackKitInventory: builder},
		job:       job,
		actionReq: RuntimeActionRequest{TenantID: "tenant-1"},
	}

	_, err := rollout.buildManagedStackKitInventory(
		context.Background(),
		[]byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{"cloud":{"offsite-object-backup":{"requirementsHash":"sha256:required"}}}}`),
		"v0.16.1", "sha256:"+strings.Repeat("b", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "S3 PutObject returned 401 Unauthorized") {
		t.Fatalf("explicit backup attestation error = %v, want fail-closed 401", err)
	}
	if !builder.called {
		t.Fatal("explicit backup requirement did not reach the attestation builder")
	}
	receipt, ok := job.Result["managed_backup_target_inventory"].(map[string]interface{})
	if !ok || receipt["status"] != "failed" || receipt["reason_code"] != "backup_target_attestation_failed" || receipt["retryable"] != false {
		t.Fatalf("backup receipt = %#v, want observable failed explicit-backup receipt", job.Result["managed_backup_target_inventory"])
	}
}

func TestManagedStackKitInventoryRejectsMalformedBackupRequirementsBeforeAuthority(t *testing.T) {
	builder := &recordingManagedStackKitInventoryBuilder{}
	job := &Job{ID: "job-malformed-backup", Result: map[string]interface{}{}}
	rollout := &deployRollout{
		cfg:       &ProvisionConfig{ManagedStackKitInventory: builder},
		job:       job,
		actionReq: RuntimeActionRequest{TenantID: "tenant-1"},
	}

	_, err := rollout.buildManagedStackKitInventory(
		context.Background(),
		[]byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":["cloud"]}`),
		"v0.16.1", "sha256:"+strings.Repeat("b", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "backup target requirements") {
		t.Fatalf("malformed backup requirements error = %v, want fail-closed rejection", err)
	}
	if builder.called {
		t.Fatal("malformed backup requirements reached the inventory authority")
	}
	receipt, ok := job.Result["managed_backup_target_inventory"].(map[string]interface{})
	if !ok || receipt["status"] != "failed" || receipt["reason_code"] != "backup_target_requirements_invalid" || receipt["retryable"] != false {
		t.Fatalf("backup receipt = %#v, want observable malformed-requirements receipt", job.Result["managed_backup_target_inventory"])
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
