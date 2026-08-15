package agentcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

func validHubCommand(commandID string) *agentpb.StackKitCommand {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return &agentpb.StackKitCommand{
		CommandId:        commandID,
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS,
		WorkingDirectory: "/srv/stack",
		ServiceKey:       "base",
		LogTail:          100,
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

	command, ok, err := hub.Poll(ctx, "agent-1", nil)
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
	command, ok, err := hub.Poll(ctx, "agent-1", nil)
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
	if _, ok, _ := hub.Poll(ctx, "agent-1", nil); !ok {
		t.Fatal("first Poll() did not dispatch")
	}
	if _, ok, _ := hub.Poll(ctx, "agent-1", nil); ok {
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
	command.ExpectedPlanHash = strings.Repeat("b", 64)
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

func TestHubRejectsReleaseSubstitutionWithoutCompletingPendingCommand(t *testing.T) {
	hub := NewHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := hub.SendStackKitCommand(ctx, "agent-1", validHubCommand("command-1"))
		errCh <- err
	}()
	command, ok, err := hub.Poll(ctx, "agent-1", nil)
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
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("SendStackKitCommand() error = %v", err)
	}
}

func TestHubRejectsUnboundResult(t *testing.T) {
	hub := NewHub()
	err := hub.SubmitResult("agent-1", &agentpb.StackKitResult{CommandId: "unknown"})
	if !errors.Is(err, ErrCommandNotPending) {
		t.Fatalf("SubmitResult() error = %v", err)
	}
}
