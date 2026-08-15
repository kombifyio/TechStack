package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitServiceArgsAreClosed(t *testing.T) {
	for _, tc := range []struct {
		operation agentpb.StackKitOperation
		want      []string
	}{
		{agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START, []string{"service", "start", "coolify", "--json", "--owner-approve"}},
		{agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP, []string{"service", "stop", "coolify", "--json", "--owner-approve"}},
		{agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART, []string{"service", "restart", "coolify", "--json", "--owner-approve"}},
	} {
		got, err := stackKitOperationArgs(&agentpb.StackKitCommand{Operation: tc.operation, ServiceKey: "coolify", OwnerApproved: true})
		if err != nil {
			t.Fatalf("%s: %v", tc.operation, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %v want %v", tc.operation, got, tc.want)
		}
	}
}

func TestStackKitServiceArgsRejectInjectionAndUnapprovedMutation(t *testing.T) {
	if _, err := stackKitOperationArgs(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START, ServiceKey: "auth;whoami", OwnerApproved: true}); err == nil {
		t.Fatal("expected invalid service key to fail")
	}
	if _, err := stackKitOperationArgs(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART, ServiceKey: "auth"}); err == nil || !strings.Contains(err.Error(), "Owner approval") {
		t.Fatalf("expected Owner approval error, got %v", err)
	}
}

func TestStackKitServiceLogArgsAreBounded(t *testing.T) {
	got, err := stackKitOperationArgs(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS, ServiceKey: "auth", LogTail: 200, LogCursor: "cursor-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"service", "logs", "auth", "--tail", "200", "--json", "--cursor", "cursor-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := stackKitOperationArgs(&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS, ServiceKey: "auth", LogTail: 201}); err == nil {
		t.Fatal("expected log tail above 200 to fail")
	}
}
