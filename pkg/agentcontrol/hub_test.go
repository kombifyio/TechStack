package agentcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

func validHubCommand(commandID string) *agentpb.StackKitCommand {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return &agentpb.StackKitCommand{
		CommandId:          commandID,
		Operation:          agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS,
		WorkingDirectory:   "/srv/stack",
		StackkitInstanceId: "stackkit-main",
		ServiceKey:         "base",
		LogTail:            100,
		Release: &agentpb.StackKitReleasePin{
			Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64",
			ArchiveSha256: digest, ReleaseIndexSha256: digest,
		},
	}
}

func validHubResult(command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return &agentpb.StackKitResult{
		CommandId: command.CommandId, Success: true, ExitCode: 0,
		Release:                    command.Release,
		CommandResultSchemaVersion: "stackkit.command-result/v1",
		CommandResultJson:          []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"service_logs","status":"success"}`),
		EventsSchemaVersion:        "stackkit.rollout-event/v1",
	}
}

func TestHubDispatchesTypedCommandAndCorrelatesResult(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan *agentpb.StackKitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("command-1"))
		if err != nil {
			errCh <- err
			return
		}
		done <- result
	}()

	command, ok, err := hub.Poll(ctx, "agent-1", []string{stackkitcommand.WorkspaceInstanceCapability})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || command.GetCommandId() != "command-1" {
		t.Fatalf("Poll() = (%v, %v)", command, ok)
	}
	if err := hub.SubmitResult("agent-1", validHubResult(command)); err != nil {
		t.Fatalf("SubmitResult() error = %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-done:
		if !result.GetSuccess() {
			t.Fatalf("result = %#v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestHubRejectsDuplicatePendingCommandID(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("command-1"))
		done <- err
	}()
	command, ok, err := hub.Poll(ctx, "agent-1", []string{stackkitcommand.WorkspaceInstanceCapability})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("first command was not dispatched")
	}
	if _, err := hub.SendStackKitCommand(ctx, "agent-2", validHubCommand("command-1")); err == nil {
		t.Fatal("Hub accepted a duplicate globally pending command_id")
	}
	if err := hub.SubmitResult("agent-1", validHubResult(command)); err != nil {
		t.Fatalf("SubmitResult() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("first SendStackKitCommand() error = %v", err)
	}
}

func TestHubDoesNotRedeliverAmbiguousCommand(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	go func() {
		_, _ = hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("command-1"))
	}()
	if _, ok, _ := hub.Poll(ctx, "agent-1", []string{stackkitcommand.WorkspaceInstanceCapability}); !ok {
		t.Fatal("first Poll() did not dispatch")
	}
	if _, ok, _ := hub.Poll(ctx, "agent-1", []string{stackkitcommand.WorkspaceInstanceCapability}); ok {
		t.Fatal("second Poll() redelivered an ambiguous apply")
	}
}

func TestHubRejectsInvalidCommandBeforeDispatch(t *testing.T) {
	hub := NewHub()
	_, err := hub.SendStackKitCommand(context.Background(), "agent-1", &agentpb.StackKitCommand{CommandId: "command-1"})
	if err == nil {
		t.Fatal("SendStackKitCommand() accepted an unpinned command")
	}
}

func TestHubRejectsApplyBeforeDispatchToAgentWithoutPlanHashCapability(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := validHubCommand("apply-1")
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY
	command.OwnerApproved = true
	command.ServiceKey = ""
	command.LogTail = 0
	command.LocalSiteRef = "cloud"
	command.LocalNodeRef = "cloud-main"
	command.LocalExecutionChannelRef = "host-channel-cloud-main"
	command.ExpectedPlanHash = "sha256:" + strings.Repeat("b", 64)
	errCh := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-old", command)
		errCh <- err
	}()
	if dispatched, found, err := hub.Poll(ctx, "agent-old", []string{"stackkit"}); err == nil || found || dispatched != nil {
		t.Fatalf("Poll() = (%v, %v, %v), want capability rejection before dispatch", dispatched, found, err)
	}
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), stackkitcommand.ExpectedPlanHashCapability) {
		t.Fatalf("SendStackKitCommand() error = %v", err)
	}
}

func TestHubRejectsReleaseSubstitutionAndTerminatesPendingCommand(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("command-1"))
		errCh <- err
	}()
	command, ok, err := hub.Poll(ctx, "agent-1", []string{stackkitcommand.WorkspaceInstanceCapability})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Poll() did not dispatch")
	}
	result := validHubResult(command)
	result.Release = proto.Clone(command.Release).(*agentpb.StackKitReleasePin)
	result.Release.ArchiveSha256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := hub.SubmitResult("agent-1", result); !errors.Is(err, ErrResultRejected) {
		t.Fatalf("SubmitResult() error = %v", err)
	}
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "release does not match") {
		t.Fatalf("SendStackKitCommand() terminal error = %v", err)
	}
}

func TestHubRejectsUnboundResult(t *testing.T) {
	hub := NewHub()
	err := hub.SubmitResult("agent-1", &agentpb.StackKitResult{CommandId: "unknown"})
	if !errors.Is(err, ErrCommandNotPending) {
		t.Fatalf("SubmitResult() error = %v", err)
	}
}

// A host maintenance command reaches only an agent that advertises
// stackkit.host-maintenance.v1: an older or unprivileged agent never receives
// it, and the sender learns why.
func TestHubNeverDispatchesHostCommandToAgentWithoutHostMaintenance(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := validHubCommand("reboot-1")
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT
	command.OwnerApproved = true
	command.WorkingDirectory, command.StackkitInstanceId, command.ServiceKey, command.LogTail = "", "", "", 0
	errCh := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-unprivileged", command)
		errCh <- err
	}()
	capabilities := []string{"stackkit", stackkitcommand.ExpectedPlanHashCapability, stackkitcommand.WorkspaceInstanceCapability}
	if dispatched, found, err := hub.Poll(ctx, "agent-unprivileged", capabilities); err == nil || found || dispatched != nil {
		t.Fatalf("Poll() = (%v, %v, %v), want the host command withheld", dispatched, found, err)
	}
	var missing *MissingCapabilityError
	if err := <-errCh; !errors.As(err, &missing) || missing.Capability != stackkitcommand.HostMaintenanceCapability {
		t.Fatalf("SendStackKitCommand() error = %v, want the missing host maintenance capability", err)
	}
}

// A worker that authenticates with a bearer credential alone may observe but
// is never handed a command that changes its host; the same command is
// dispatched to a key-bound worker.
func TestHubWithholdsMutatingCommandsFromObserveOnlyWorkers(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apply := validHubCommand("apply-1")
	apply.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY
	apply.OwnerApproved = true
	apply.ServiceKey, apply.LogTail = "", 0
	apply.LocalSiteRef, apply.LocalNodeRef, apply.LocalExecutionChannelRef = "cloud", "cloud-main", "host-channel-cloud-main"
	apply.ExpectedPlanHash = "sha256:" + strings.Repeat("b", 64)
	capabilities := append(stackkitcommand.RequiredAgentCapabilities(apply), stackkitcommand.WorkspaceInstanceCapability)
	go func() { _, _ = hub.SendStackKitCommand(ctx, "agent-1", apply) }()
	go func() { _, _ = hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("logs-1")) }()
	time.Sleep(100 * time.Millisecond)

	observe := func(wait time.Duration) string {
		pollCtx, stop := context.WithTimeout(ctx, wait)
		defer stop()
		offer, found, err := hub.PollBoundForTenant(pollCtx, PollOptions{AgentID: "agent-1", Capabilities: capabilities, ObserveOnly: true})
		if err != nil || !found {
			return ""
		}
		return offer.Command.GetCommandId()
	}
	if got := observe(time.Second); got != "logs-1" {
		t.Fatalf("observe-only poll = %q, want the read-only command", got)
	}
	if got := observe(300 * time.Millisecond); got != "" {
		t.Fatalf("observe-only poll was handed %q", got)
	}
	offer, found, err := hub.PollBoundForTenant(ctx, PollOptions{AgentID: "agent-1", Capabilities: capabilities})
	if err != nil || !found || offer.Command.GetCommandId() != "apply-1" {
		t.Fatalf("key-bound poll = %+v, %v, %v", offer, found, err)
	}
}

// The in-memory hub binds what it offers a bound Guard to that Guard, and a
// Guard's refusal ends the caller's wait instead of leaving it to time out.
func TestInMemoryHubBindsOffersAndHonoursGuardRefusal(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := validHubCommand("logs-1")
	errCh := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-1", command)
		errCh <- err
	}()
	capabilities := append(stackkitcommand.RequiredAgentCapabilities(command), guardbinding.Capability)
	var offer *Offer
	for offer == nil {
		pollCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		got, found, err := hub.PollBoundForTenant(pollCtx, PollOptions{TenantID: "tenant-1", AgentID: "agent-1", Capabilities: capabilities})
		stop()
		if err != nil && ctx.Err() != nil {
			t.Fatalf("PollBoundForTenant() error = %v", err)
		}
		if found {
			offer = got
		}
	}
	if offer.Binding == nil {
		t.Fatal("a bound Guard was offered a command without a binding")
	}
	raw, err := json.Marshal(offer.Command)
	if err != nil {
		t.Fatal(err)
	}
	if denial := offer.Binding.Validate(raw, offer.Command.Operation.String(), guardbinding.Expectation{TenantID: "tenant-1", WorkerID: "agent-1"}, time.Now()); denial != nil {
		t.Fatalf("the Guard would refuse its own binding: %v", denial)
	}
	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportRejected, Reason: "binding mismatch"}
	if _, err := hub.SubmitReportForTenant(ctx, ReportRequest{TenantID: "tenant-1", AgentID: "agent-1", CommandID: "logs-1", Report: report}); err != nil {
		t.Fatalf("SubmitReportForTenant(rejected) error = %v", err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrResultRejected) {
			t.Fatalf("SendStackKitCommand() error = %v, want the Guard's refusal", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the caller is still waiting after the Guard refused the command")
	}
}
