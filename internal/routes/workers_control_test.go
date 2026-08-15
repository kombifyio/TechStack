package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

func TestTypedHTTPSControlAuthenticatesDispatchesAndCorrelates(t *testing.T) {
	const token = "opaque-runtime-token"
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Status: "connected", Approved: true,
		Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
		Capabilities: map[string]any{"server_id": "server-1", "runtime_agent_id": "runtime-1"},
	}); err != nil {
		t.Fatal(err)
	}
	hub := agentcontrol.NewHub()
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, TypedControl: hub})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	resultDone := make(chan *agentpb.StackKitResult, 1)
	errDone := make(chan error, 1)
	digest := strings.Repeat("a", 64)
	command := &agentpb.StackKitCommand{
		CommandId:        "command-1",
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS,
		WorkingDirectory: "/srv/stack",
		ServiceKey:       "base",
		LogTail:          100,
		Release: &agentpb.StackKitReleasePin{
			Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64",
			ArchiveSha256: digest, ReleaseIndexSha256: digest,
		},
	}
	go func() {
		result, err := hub.SendStackKitCommand(ctx, "runtime-1", command)
		if err != nil {
			errDone <- err
			return
		}
		resultDone <- result
	}()

	poll := performTypedControlRequest(t, router, "/api/v1/workers/runtime-1/commands/next", token, map[string]any{
		"runtime_agent_id": "runtime-1", "tenant_id": "tenant-1", "owner_id": "owner-1", "stack_id": "stack-1", "server_id": "server-1",
	})
	if poll.Code != http.StatusOK {
		t.Fatalf("poll status = %d body=%s", poll.Code, poll.Body.String())
	}
	var pollBody struct {
		Data struct {
			Command *agentpb.StackKitCommand `json:"command"`
		} `json:"data"`
	}
	if err := json.Unmarshal(poll.Body.Bytes(), &pollBody); err != nil || pollBody.Data.Command.GetCommandId() != "command-1" {
		t.Fatalf("poll body = %s err=%v", poll.Body.String(), err)
	}

	result := performTypedControlRequest(t, router, "/api/v1/workers/runtime-1/commands/result", token, map[string]any{
		"runtime_agent_id": "runtime-1", "tenant_id": "tenant-1", "owner_id": "owner-1", "stack_id": "stack-1", "server_id": "server-1",
		"result": map[string]any{
			"command_id": "command-1", "success": true,
			"release":                       command.Release,
			"command_result_schema_version": "stackkit.command-result/v1",
			"command_result_json":           []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"service_logs","status":"success"}`),
			"events_schema_version":         "stackkit.rollout-event/v1",
		},
	})
	if result.Code != http.StatusAccepted {
		t.Fatalf("result status = %d body=%s", result.Code, result.Body.String())
	}
	select {
	case err := <-errDone:
		t.Fatal(err)
	case accepted := <-resultDone:
		if !accepted.GetSuccess() {
			t.Fatalf("accepted result = %#v", accepted)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestTypedHTTPSControlRejectsMissingBearer(t *testing.T) {
	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, TypedControl: agentcontrol.NewHub()})
	recorder := performTypedControlRequest(t, router, "/api/v1/workers/runtime-1/commands/next", "", map[string]any{"runtime_agent_id": "runtime-1", "tenant_id": "tenant-1"})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func performTypedControlRequest(t *testing.T, router http.Handler, path, token string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
