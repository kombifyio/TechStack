package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/go-common/runtimeexecutor"
)

func TestStackKitOperationsProcessUsesFixedAuthenticatedBoundary(t *testing.T) {
	var receivedAuthorization, receivedAgent, receivedTenant string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/workers/runtime-1/stackkit/operations" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		receivedAuthorization = request.Header.Get("Authorization")
		receivedAgent = request.Header.Get("X-Kombify-Runtime-Agent-ID")
		receivedTenant = request.Header.Get("X-Kombify-Tenant-ID")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(stackKitOperationsResponseEnvelope{
			SchemaVersion: stackKitOperationsResponseSchema, ChannelRef: stackKitOperationsChannelRef,
			Outcome: runtimeexecutor.ExecutionOutcome{},
		})
	}))
	defer server.Close()
	enrollmentPath := writeOperationsEnrollment(t, server.URL)
	payload := encodeOperationsRequest(t, "stack-1")
	var output bytes.Buffer
	if err := executeStackKitOperationsProcess(context.Background(), bytes.NewReader(payload), &output, enrollmentPath, server.Client()); err != nil {
		t.Fatal(err)
	}
	if receivedAuthorization != "Bearer secret-token" || receivedAgent != "runtime-1" || receivedTenant != "tenant-1" {
		t.Fatalf("auth boundary = %q %q %q", receivedAuthorization, receivedAgent, receivedTenant)
	}
	if !strings.Contains(output.String(), stackKitOperationsResponseSchema) {
		t.Fatalf("output = %s", output.String())
	}
}

func TestStackKitOperationsProcessRejectsStackOrChannelEscapeBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("escaped request reached HTTP boundary")
	}))
	defer server.Close()
	for name, mutate := range map[string]func(*stackKitOperationsRequestEnvelope){
		"stack": func(request *stackKitOperationsRequestEnvelope) {
			request.Request.BackupTargetBindings[0].StackID = "other"
		},
		"channel": func(request *stackKitOperationsRequestEnvelope) {
			request.Request.RuntimeTargets[0].ExecutionChannelRef = "other"
		},
		"site": func(request *stackKitOperationsRequestEnvelope) {
			request.Request.RuntimeTargets[0].SiteRefs = []string{"home"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := operationsRequest("stack-1")
			mutate(&request)
			payload, _ := json.Marshal(request)
			err := executeStackKitOperationsProcess(context.Background(), bytes.NewReader(payload), &bytes.Buffer{}, writeOperationsEnrollment(t, server.URL), server.Client())
			if err == nil || !strings.Contains(err.Error(), "escaped") {
				t.Fatalf("expected escape rejection, got %v", err)
			}
		})
	}
}

func TestStackKitOperationsProcessPreservesActionableRejectionEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"error":{"code":"STACKKIT_OPERATION_REJECTED","message":"Managed StackKits operation was rejected","details":{"reason_code":"backup_binding_stale","retryable":true,"user_guidance":"Regenerate the StackKits Inventory."}}}`))
	}))
	defer server.Close()
	err := executeStackKitOperationsProcess(
		context.Background(),
		bytes.NewReader(encodeOperationsRequest(t, "stack-1")),
		&bytes.Buffer{},
		writeOperationsEnrollment(t, server.URL),
		server.Client(),
	)
	if err == nil || !strings.Contains(err.Error(), "reason=backup_binding_stale") ||
		!strings.Contains(err.Error(), "retryable=true") ||
		!strings.Contains(err.Error(), "guidance=Regenerate the StackKits Inventory.") {
		t.Fatalf("actionable rejection = %v", err)
	}
}

func TestStackKitOperationsEndpointRejectsInsecureRemoteAndIdentityDrift(t *testing.T) {
	enrollment := agentEnrollment{RuntimeAgentID: "runtime-1", HeartbeatURL: "http://example.com/api/v1/workers/runtime-1/heartbeat"}
	if _, err := stackKitOperationsEndpoint(enrollment); err == nil {
		t.Fatal("insecure remote endpoint accepted")
	}
	enrollment.HeartbeatURL = "https://example.com/api/v1/workers/other/heartbeat"
	if _, err := stackKitOperationsEndpoint(enrollment); err == nil {
		t.Fatal("runtime identity drift accepted")
	}
}

func TestStackKitOperationsProcessModeRequiresInstalledPrivateCopyBasename(t *testing.T) {
	name := "techstack-stackkit-operations"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if !isStackKitOperationsProcessMode([]string{filepath.Join("tmp", name)}) {
		t.Fatal("private operations basename not detected")
	}
	if isStackKitOperationsProcessMode([]string{"techstack"}) ||
		isStackKitOperationsProcessMode([]string{"channel-executor"}) ||
		isStackKitOperationsProcessMode([]string{name, "arg"}) {
		t.Fatal("ordinary invocation entered operations mode")
	}
}

func writeOperationsEnrollment(t *testing.T, serverURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-enrollment.json")
	body := `{"data":{"runtime_agent_id":"runtime-1","tenant_id":"tenant-1","owner_id":"owner-1","stack_id":"stack-1","agent_token":"secret-token","heartbeat_url":"` + serverURL + `/api/v1/workers/runtime-1/heartbeat","inventory_url":"` + serverURL + `/api/v1/workers/runtime-1/inventory"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encodeOperationsRequest(t *testing.T, stackID string) []byte {
	t.Helper()
	payload, err := json.Marshal(operationsRequest(stackID))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func operationsRequest(stackID string) stackKitOperationsRequestEnvelope {
	const requirement = "requirement-1"
	const bindingID = "binding-1"
	return stackKitOperationsRequestEnvelope{SchemaVersion: stackKitOperationsRequestSchema, ChannelRef: stackKitOperationsChannelRef,
		Request: runtimeexecutor.ExecutionRequest{
			RuntimeTargets: []runtimeexecutor.RuntimeTarget{{RequirementID: requirement, ExecutionChannelRef: stackKitOperationsChannelRef,
				SiteRefs: []string{stackKitOperationsSiteRef}, NodeRefs: []string{stackKitOperationsNodeRef}, BackupTargetBindingRefs: []string{bindingID}}},
			BackupTargetBindings: []runtimeexecutor.BackupTargetBinding{{ID: bindingID, RuntimeRequirementID: requirement, StackID: stackID,
				SiteRef: stackKitOperationsSiteRef, TargetNodeRefs: []string{stackKitOperationsNodeRef}}},
		}}
}
