package jobs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestNormalizeStackKitServiceLifecycleRequest(t *testing.T) {
	req, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceLogs, ServiceKey: "auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.LogTail != 100 {
		t.Fatalf("default log tail = %d, want 100", req.LogTail)
	}
	if _, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceStop, ServiceKey: "auth",
	}); err == nil {
		t.Fatal("expected mutation without Owner approval to fail")
	}
}

func TestStackKitServiceActionDurableIdentityAndLogProjection(t *testing.T) {
	digest := strings.Repeat("a", 64)
	req, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceLogs, ServiceKey: "auth", ServiceID: "service-1",
		DurableJobID: StackKitServiceActionJobID("tenant-1", "owner-1", "logs-1"), ServiceActionDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.DurableJobID != StackKitServiceActionJobID("tenant-1", "owner-1", "logs-1") || strings.Contains(req.DurableJobID, "logs-1") {
		t.Fatalf("durable job ID leaked or drifted: %q", req.DurableJobID)
	}
	page, _ := json.Marshal(map[string]any{
		"apiVersion": "stackkit.service-logs/v1", "serviceKey": "auth",
		"entries": []any{map[string]any{"timestamp": "2026-08-10T12:00:00Z", "message": "ready"}},
	})
	envelope, _ := json.Marshal(map[string]any{
		"schemaVersion": "stackkit.command-result/v1", "command": "service_logs", "status": "success",
		"data": map[string]any{"output": string(page)},
	})
	result, err := normalizeStackKitLifecycleResult(req, &agentpb.StackKitResult{Success: true, CommandResultJson: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if !MatchesStackKitServiceActionReceipt(result, req) {
		t.Fatalf("durable receipt missing: %#v", result)
	}
	logs, _ := result["service_logs"].(map[string]any)
	if stringFromInterface(logs["serviceKey"]) != "auth" {
		t.Fatalf("service logs missing: %#v", result)
	}
}
